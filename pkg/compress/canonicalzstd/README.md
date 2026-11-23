# Canonical Zstandard

This document specifies a canonical way to compress data using the
[Zstandard format (RFC 8878)](https://www.rfc-editor.org/rfc/rfc8878).
For every input, this specification yields exactly one compressed
output. Any conforming Zstandard decoder can decompress it.

The Go package in this directory is the reference implementation.

## Motivation

Bonanza stores data in a content addressed store. Objects are
identified by a hash of their encoded contents. If two clients encode
the same data differently, the objects get different references. This
breaks deduplication, and it means that the result of a build depends on
the exact version of the software that produced it.

RFC 8878 only specifies the format of compressed data and how to decode
it. It leaves almost every encoding decision to the encoder:

- the match finder,
- block splitting,
- entropy table construction,
- the choice between table modes,
- the initial FSE state.

As a result, the output of `libzstd`, `klauspost/compress` and other
implementations differs. It often even differs between releases of the
same implementation. This is why Bonanza originally used "simple LZW":
its output is easy to reproduce, but it compresses noticeably worse than
Zstandard.

This specification removes every degree of freedom from the encoder.
Where it relies on heuristic data structures, such as the hash table
used to find matches, it defines their size, hash function and update
rules exactly. Implementations are free to organize their code
differently, as long as they produce the same output.

## Design constraints

Bonanza never needs to compress or decompress data in a streaming
fashion. Objects are at most 2 MiB and always fit in memory. This lets
the canonical encoding make choices that streaming compressors cannot:

- **The window spans the entire input.** Frames are encoded as a single
  segment, so matches can refer to any earlier byte of the input. Frames
  therefore have no Window_Descriptor.
- **The content size is always known up front.** It is stored in the
  frame header, which lets decoders allocate exactly the right amount of
  memory. This replaces the varint size prefix used by simple LZW.
- **Entropy coding decisions are made by measuring exact sizes.** The
  parse of a block is complete before any entropy coding takes place. As
  a result, the encoder can try every candidate table encoding and pick
  the one that is actually smallest instead of using heuristics. This is
  both better and easier to specify than the heuristics of streaming
  compressors.
- **No checksum.** Bonanza objects are already content addressed.

One limit does remain. The format caps blocks at 128 KiB, so inputs
larger than that are still split into multiple blocks.

## Notation

- `n` is the size of the input in bytes, and `data[i]` is its byte at
  position `i`.
- `bitlen(x)` is the number of bits needed to represent `x`, i.e.
  `floor(log2(x)) + 1` for `x > 0`.
- "Smallest" means smallest in bytes, unless stated otherwise.
- Section references, like § 3.1.1, refer to RFC 8878.

## Frame

The output is a single Zstandard frame. It starts with the 4-byte magic
number `28 B5 2F FD`, followed by a frame header (§ 3.1.1.1) that is
laid out as follows:

- Frame_Header_Descriptor:
  - Single_Segment_Flag = 1,
  - Content_Checksum_Flag = 0,
  - Dictionary_ID_Flag = 0,
  - Unused and Reserved bits = 0,
  - Frame_Content_Size_Flag set to the smallest representation that fits
    `n`:

    | `n`               | Flag | FCS field             |
    | ----------------- | ---- | --------------------- |
    | 0–255             | 0    | 1 byte, `n`           |
    | 256–65791         | 1    | 2 bytes, `n - 256`    |
    | 65792–2^32-1      | 2    | 4 bytes, `n`          |
    | larger            | 3    | 8 bytes, `n`          |

- There is no Window_Descriptor, Dictionary_ID or checksum.

**An empty input is encoded as empty output**, and empty input is
decoded as empty output. Many parts of Bonanza give empty data a special
meaning, so it must stay empty after encoding. This is unambiguous,
because a frame is never empty. This is the only case where the output
is not a valid Zstandard frame. A decoder that needs a standard frame
for empty input can use `28 B5 2F FD 20 00 01 00 00`: a frame with a
content size of 0, holding a single empty last Raw block.

## Blocks

The input is split into blocks of exactly 131072 bytes (128 KiB). The
final block holds the remainder. The last block has Last_Block set.

The block type is chosen as follows:

1. If the block is at least 2 bytes long and all of its bytes are equal,
   it's encoded as an **RLE block**.
2. Otherwise, the block is parsed into sequences ([Parsing](#parsing))
   and encoded as a compressed block. If its Block_Content is strictly
   smaller than the block's size, it's emitted as a **Compressed
   block**.
3. Otherwise, it's emitted as a **Raw block**.

Matches may refer to data in earlier blocks, irrespective of how those
blocks were encoded. However, the repeated offsets (§ 3.1.2.5) only
change when a compressed block is emitted. If a block is emitted as Raw
or RLE, the repeated offsets go back to their values from before the
block was parsed.

The repeated offsets start out as `1, 4, 8` at the beginning of the
frame.

## Parsing

A block covering positions `[start, end)` is turned into a list of
sequences `(literal_length, match_length, offset_value)` plus trailing
literals. The rules below use the current repeated offsets `rep[0..2]`
and the current literal length `ll`, which is the number of literals
since the last match or since the start of the block.

### Offset values

The **offset value** of an offset `o` at literal length `ll` is the
smallest Offset_Value (§ 3.1.1.5) that encodes `o`:

- If `ll > 0`:
  - `1` if `o == rep[0]`,
  - otherwise `2` if `o == rep[1]`,
  - otherwise `3` if `o == rep[2]`.
- If `ll == 0`:
  - `1` if `o == rep[1]`,
  - otherwise `2` if `o == rep[2]`,
  - otherwise `3` if `o == rep[0] - 1`.
- Otherwise `o + 3`.

### Valid matches

A match at position `p` with offset `o` is valid if all of the
following hold:

- `1 ≤ o ≤ p`.
- Its length `L` is at least 4. `L` is the largest value such that
  `p + L ≤ end` and `data[p+i] == data[p+i-o]` for all `0 ≤ i < L`.
  Matches may overlap the data being produced (`o < L`), but may not
  extend beyond the end of the block.

The **score** of a match is `4 × length − bitlen(offset_value)`. This
roughly approximates the number of bytes it saves, in units of a quarter
byte. Among a set of valid matches, the best match is the one with:

1. the highest score, then
2. the greatest length, then
3. the smallest offset value.

### Hash table

Matches are found using a hash table that persists across all blocks of
the frame. If `n < 4`, no hash table is used and no matches are emitted.
Otherwise:

- The table consists of `2^T` buckets, where
  `T = min(max(ceil(log2(n)) − 1, 8), 16)`.
- Each bucket has two slots, which are initially empty.
- The bucket of position `q` is
  `(x × 0x9E3779B1 mod 2^32) >> (32 − T)`, where `x` is the little
  endian 32-bit integer formed by `data[q..q+4)`.

**Inserting** position `q` does nothing if `q + 4 > n`. Otherwise, it
moves the contents of the first slot of `q`'s bucket to the second slot,
discarding the second slot's previous contents, and then stores `q` in
the first slot.

### Greedy parsing

Parsing is greedy: the best match at a position is always emitted. When
there is no match, the parser skips ahead by an amount that grows with
the number of literals since the last match, up to 32 positions. This
makes compressing incompressible data cheap. The limit matters, because
skipped positions are not inserted into the hash table. Without it, a
long run of literals would make the parser skip ever further, leaving
too few positions in the hash table to find matches once the data
becomes compressible again. Parsing proceeds as follows:

```
literals_start = start
p = start
while p + 4 ≤ end:
    ll = p - literals_start
    candidates = {
        the repeated offset with offset value 1:
            rep[0] if ll > 0, or rep[1] if ll == 0,
        p - q for each position q stored in the bucket of p,
    }
    m = the best valid match among the candidates
    insert(p)
    if m does not exist:
        p += 1 + min(floor(ll / 16), 31)
        continue
    emit sequence (ll, m.length, m.offset_value)
    update repeated offsets (§ 3.1.2.5)
    insert(p + 2)
    if p + m.length - 2 > p + 2:
        insert(p + m.length - 2)
    p += m.length
    literals_start = p
trailing literals = data[literals_start .. end)
```

The offset value of each candidate is computed as described in
[Offset values](#offset-values). A candidate that comes from the hash
table may thus also be encoded as a repeat code. The bucket is read
before `p` is inserted.

## Literals section

The literals of a block are all `data[literals_start .. p)` ranges
of the sequences, concatenated together with the trailing literals.
The literals section (§ 3.1.1.3.1) is the smallest of the following
candidates. On ties, the earlier candidate in this list wins:

1. **Raw_Literals_Block.**
2. **RLE_Literals_Block**, if there is at least one literal and all
   literals are equal.
3. **Compressed_Literals_Block**, if there are at least two distinct
   literal values and a tree description can be formed (see
   [Huffman tree description](#huffman-tree-description)).

Treeless_Literals_Block is never used.

For Raw and RLE, Size_Format uses the smallest header that fits:

- 1 byte (Size_Format `00`) if the size is less than 32,
- 2 bytes (`01`) if it is less than 4096,
- 3 bytes (`11`) otherwise.

### Huffman code lengths

Code lengths are computed using the **package-merge algorithm**, with a
maximum code length of 11. The steps are:

1. Create a leaf for each byte value that occurs, weighted by its number
   of occurrences. Sort the leaves by weight, then by byte value,
   ascending.
2. Let list `A` be the leaves.
3. Repeat 10 times:
   1. Form packages by pairing consecutive items of `A`: (A[0], A[1]),
      (A[2], A[3]), and so on. A trailing unpaired item is discarded.
      The weight of a package is the sum of its two items.
   2. Merge the leaves and the packages into a new list `A`, sorted by
      weight. When a leaf and a package have equal weight, **the leaf
      comes first**. Leaves keep their relative order, and so do
      packages.
4. Take the first `2k − 2` items of `A`, where `k` is the number of
   leaves. The code length of a byte value is the number of times its
   leaf occurs in these items, counting through packages recursively.

`Max_Number_of_Bits` is the largest resulting code length. The weight of
a byte value with code length `l` is `Max_Number_of_Bits + 1 − l`, and
0 if the value does not occur. Prefix codes follow from the weights as
specified in § 4.2.1.3.

### Huffman tree description

Let `last` be the largest byte value with a non-zero weight. Weights
are transmitted for byte values `0 .. last−1`. The weight of `last` is
implied. The tree description is the smallest of the following. On
ties, the earlier option wins:

1. **Direct representation** (§ 4.2.1.1), if at most 128 weights are
   transmitted. The header byte is `127 + count`, followed by the
   weights packed two per byte with the first in the high nibble. If the
   count is odd, the low nibble of the last byte is 0.
2. **FSE compressed representation**, if at least 3 weights are
   transmitted and they contain at least 2 distinct values. This
   restriction exists because decoders cannot reliably determine the
   number of weights otherwise.
   - For each accuracy log in {5, 6}:
     1. Normalize the weight counts as described in
        [FSE normalization](#fse-normalization). Only symbols `0` up to
        the largest weight present are included.
     2. Write the table description as described in
        [FSE table description](#fse-table-description).
     3. Encode the weights with two interleaved states: weight `i` uses
        state `i mod 2`.
        - Weights are encoded from last to first.
        - The last weight of each state is not encoded. Instead, it
          sets that state's initial value, as described in
          [FSE initial states](#fse-initial-states).
        - After encoding, flush state 1 followed by state 0, then close
          the bitstream (§ 4.1).

     The candidate is valid if its compressed size is at most 127 bytes.
   - The smallest valid candidate is used. On ties, accuracy log 5
     wins.

If neither option is available, Compressed_Literals_Block is not a
candidate.

### Streams

The literals are Huffman coded into a bitstream as described in
§ 4.2.2. The literals are written last to first, and the stream is
closed with a single 1 bit and zero padding.

- If there are fewer than 1024 literals, encode them as a **single
  stream**. If the tree description plus the stream is smaller than
  1024 bytes, use Size_Format `00`.
- Otherwise, use **four streams**. The first three each hold
  `(count + 3) / 4` literals (integer division), and the fourth holds
  the rest.

  Size_Format is the smallest of `01`, `10` and `11` that fits both the
  regenerated size and the compressed size, which covers the tree
  description, the jump table and the streams.

## Sequences section

Number_of_Sequences (§ 3.1.1.3.2.1) uses its shortest encoding:

- 1 byte if the count is less than 128,
- 2 bytes if it is less than 0x7F00,
- 3 bytes otherwise.

If there are no sequences, the section ends here.

Literal lengths, offsets and match lengths are converted to codes as
described in § 3.1.1.3.2.1.1 and § 3.1.1.3.2.1.2. For each of the three
code streams, the compression mode is chosen independently. The chosen
mode minimizes `cost = 8 × (description size in bytes) + (state bits)`.
"State bits" is the total number of bits that the FSE encoder emits for
that stream (see [FSE encoding](#fse-encoding)), including the final
state flush. It excludes the extra bits, as those are the same for every
mode.

The candidates are, in tie-breaking order:

1. **Predefined_Mode**, if every code that occurs has a non-zero
   probability in the predefined distribution (§ 3.1.1.3.2.2). The
   description size is 0.
2. **RLE_Mode**, if exactly one distinct code occurs. The description
   size is 1, and the number of state bits is 0.
3. **FSE_Compressed_Mode**, if at least two distinct codes occur. Let
   `N` be the number of sequences. The accuracy log is
   `max(5, ceil(log2(distinct codes)), min(M, ceil(log2(N)) − 2))`,
   where `M` is the maximum accuracy log: 9 for literal lengths and
   match lengths, and 8 for offsets. Counts are normalized as described in
   [FSE normalization](#fse-normalization), with the alphabet running
   from code 0 up to the largest code that occurs.

Repeat_Mode is never used, which keeps every block's entropy tables
independent of earlier blocks.

The bitstream is produced exactly as in § 3.1.1.3.2.1 and § 3.1.1.4:

1. The last sequence's codes set the initial state of each table (see
   [FSE initial states](#fse-initial-states)). Its extra bits are
   written in the order literal length, match length, offset.
2. For each earlier sequence, from last to first:
   1. Encode its offset, match length and literal length codes, in
      that order.
   2. Write its literal length, match length and offset extra bits, in
      that order.
3. Flush the match length, offset and literal length states, in that
   order.
4. Close the bitstream.

## FSE

### FSE normalization

Given occurrence counts `c[s]` with total `T`, and an accuracy log `A`,
the normalized counts `N[s]` sum to `2^A`. They are computed with
integer arithmetic only:

1. For each symbol with `c[s] > 0`, let
   `q = floor(c[s] × 2^A / T)` and `r = (c[s] × 2^A) mod T`.
   - If `q == 0`, set `N[s] = 1`. The symbol is "rounded up".
   - Otherwise set `N[s] = q`, and remember the remainder `r`.
2. Let `S = Σ N[s]`.
   - If `S < 2^A`: sort the symbols that were not rounded up by
     remainder (descending), then by symbol (ascending). Add 1 to the
     first `2^A − S` of them.
   - If `S > 2^A`: repeat `S − 2^A` times: subtract 1 from the symbol
     with the largest `N[s]`, choosing the smallest symbol on ties.

The "less than 1" probability (`-1`) is never produced. It only occurs
in the predefined distributions.

### FSE table description

Table descriptions are written exactly as specified in § 4.1.1:

- After a zero probability, the run of zero probabilities that follows
  is encoded with as many repeat flags of value 3 as possible, followed
  by one flag holding the remainder.
- The description ends as soon as the remaining probability reaches 0.
- The final byte is padded with zero bits.

This matches the output of `FSE_writeNCount()` in the reference
implementation.

### FSE encoding

FSE encoding tables are derived from the normalized counts exactly as
the reference implementation's `FSE_buildCTable()` does. This is fully
determined by the decoding table (§ 4.1.1). Symbols are encoded with the
usual tANS step:

```
nbBits = (state + deltaNbBits[s]) >> 16
write the low nbBits bits of state
state = stateTable[(state >> nbBits) + deltaFindState[s]]
```

Flushing a state writes its low `A` bits.

### FSE initial states

Every stream ends with a symbol that is never encoded, because the
decoder obtains it from its initial state. The encoder therefore sets
its initial state to **the lowest position in the decoding table that
decodes to that symbol**. In encoder terms, that's `2^A + position`.

This is also what `FSE_initCState2()` in the reference implementation
computes. It has one important property: for any distribution with at
least two symbols, the state at that position always reads at least one
bit. Decoders of the two-state Huffman weight streams rely on this to
detect the end of the stream.

## Decoding and validation

Any RFC 8878 decoder can decode the output. Before decompressing,
decoders should check that:

- Frame_Content_Size does not exceed the maximum size they accept.
- The input starts with the regular frame magic number, not that of a
  skippable frame.
- **The input consists of exactly one frame.** Many decoders decode
  all frames contained in their input. Without this check, a small input
  consisting of a valid frame followed by additional frames can expand
  to an arbitrarily large output. This check only requires walking the
  block headers: the last block must end exactly at the end of the
  input.

The reference implementation also rejects blocks that are larger than
`min(Frame_Content_Size, 128 KiB)`, and Raw and RLE blocks whose
combined size exceeds Frame_Content_Size. It relies on the decoder to
ensure that the output of Compressed blocks does not exceed
Frame_Content_Size.

A decoder does not automatically check that its input is canonical. In
most contexts that is fine: non-canonical input still decodes to the
right data, and only deduplication suffers. If canonicality must be
enforced, the decoder should re-compress the output and compare the
result with its input.

## Results

The table below compares compression ratios: compressed size divided by
uncompressed size, so lower is better. The inputs are Go source code,
generated `.pb.go` files, an arm64 Mach-O executable and Go archives
(`.a` files) taken from the Go build cache. Each was split into chunks,
and every chunk was compressed separately. Bonanza currently compresses
only file chunks, which are content-defined with a minimum of 64 KiB and
a horizon of 512 KiB, so those sizes matter most. The Zstandard figures
from `klauspost/compress` exclude the checksum.

| Encoder                          | src 64K | src 512K | pb.go 64K | pb.go 512K | binary 64K | binary 512K | .a 64K | .a 512K |
| -------------------------------- | ------: | -------: | --------: | ---------: | ---------: | ----------: | -----: | ------: |
| simple LZW                       | 0.368   | 0.315    | 0.284     | 0.217      | 0.666      | 0.676       | 0.318  | 0.319   |
| klauspost `SpeedFastest`         | 0.234   | 0.213    | 0.133     | 0.118      | 0.586      | 0.572       | 0.209  | 0.203   |
| klauspost `SpeedDefault`         | 0.218   | 0.194    | 0.126     | 0.109      | 0.567      | 0.560       | 0.201  | 0.198   |
| klauspost `SpeedBestCompression` | 0.194   | 0.164    | 0.111     | 0.093      | 0.545      | 0.535       | 0.185  | 0.180   |
| **canonical Zstandard**          | 0.231   | 0.214    | 0.129     | 0.114      | 0.564      | 0.557       | 0.207  | 0.207   |

The canonical encoding produces output that is 32–55% smaller than
simple LZW on text, 15–18% smaller on machine code and 35% smaller on Go
archives. Its compression
is on par with klauspost's fastest level.

Bonanza computes the references of objects to perform cache lookups,
which requires compressing them. This is why the canonical encoding
favors compression speed over compression ratio. On an Apple M3 Pro,
compression throughput is:

| Encoder                  | src 64K  | src 512K | pb.go 64K | pb.go 512K | binary 64K | binary 512K | .a 64K   | .a 512K  |
| ------------------------ | -------: | -------: | --------: | ---------: | ---------: | ----------: | -------: | -------: |
| simple LZW               | 104 MB/s | 87 MB/s  | 118 MB/s  | 102 MB/s   | 70 MB/s    | 25 MB/s     | 102 MB/s | 84 MB/s  |
| klauspost `SpeedFastest` | 445 MB/s | 430 MB/s | 807 MB/s  | 804 MB/s   | 464 MB/s   | 496 MB/s    | 584 MB/s | 548 MB/s |
| klauspost `SpeedDefault` | 394 MB/s | 399 MB/s | 758 MB/s  | 853 MB/s   | 335 MB/s   | 331 MB/s    | 481 MB/s | 450 MB/s |
| **canonical Zstandard**  | 229 MB/s | 243 MB/s | 356 MB/s  | 411 MB/s   | 180 MB/s   | 171 MB/s    | 234 MB/s | 237 MB/s |

This is 2–7× as fast as simple LZW, and 0.35–0.55× as fast as
klauspost's fastest level. Variance between runs is around ±10%. Simple
LZW's speed degrades on large incompressible inputs, because its
dictionary keeps growing. Decompression runs at the speed of a regular
Zstandard decoder, which is considerably faster than simple LZW.

The parameters were chosen by measuring. Compared to the alternatives
that were considered:

- **An exhaustive search** with lazy matching, over the 16 most recent
  positions sharing the same four byte prefix, produced output that was
  15–22% smaller on text and 6–7% smaller on binaries. However, it
  compressed at only 37–86 MB/s.
- **Two slots per bucket** produce output that is 5–8% smaller on text
  than a single slot, while being 14–16% slower. The output for binaries
  is less than 1% smaller.
- **Only considering one repeated offset** instead of all three makes
  output 0.4–0.8% larger, and is 5–25% faster.
- **Sizing the hash table** relative to the input avoids clearing a
  large table for small objects. Tables larger than `2^16` buckets did
  not improve compression measurably.
- **Skipping** during runs of literals mostly speeds up incompressible
  data. Skipping twice as fast makes binaries compress 1.2–1.3× as
  fast, but makes output 5–8% larger, and up to 15% larger for 4 KiB
  inputs.
- **Limiting the skip** to 32 positions costs 12–16% of the speed on
  incompressible data. Without the limit, some inputs barely compress:
  a 66 KB Go archive whose first part contains few matches compressed to
  75% of its size instead of 50%. On the Go archives and the executable,
  the limit makes output 2–5% smaller. A limit of 8 positions compresses
  equally well, but halves the speed on incompressible data.
- **A single FSE accuracy log** compresses within 0.1% of trying every
  accuracy log.

Most of the remaining gap with klauspost's fastest level is in the
implementation, not in the format. For example, klauspost's encoder
estimates the size of entropy coded data, while the canonical encoding
requires computing it exactly. The reference implementation reduces
this cost by first computing bounds on the number of state bits from
the symbol counts, as every symbol emits either `k` or `k + 1` bits for
a fixed `k`. Encoding only needs to be simulated if these bounds don't
determine the cheapest mode, which is the case for about 10% of the
streams.
