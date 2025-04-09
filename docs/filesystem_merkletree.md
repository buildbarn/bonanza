## Notes on how Bonanza encodes trees of files into DAGS of objects.
## Author: Lucas Meijer. I spend a lot of time figuring all this out while reading the bonanza codebase, and wrote it down so you can speed through it more quickly.

- A file will always live alone in an object. It will never share an object with any other file or other thing.
- A file might span multiple objects. In this case there will be another object that has an array of FileContents messages
  that point to all the objects that the file spans.
- The only way for anything to point to a file is using a FileContents message
 
The directory structure gets stored differently: using the protobuf messages
described in `/pkg/proto/model/filesystem`.

The process for creating these messages and putting them into objects lives in
`/pkg/model/filesystem/create_directory_merkle_tree.go`

That code needs to balance several concerns:
- The maximum depth of the DAG of produced objects needs to be minimal.
  This is important because when you upload a dag, the protocol needs to go
  back and forth to discover more objects. The deeper the DAG, the more roundtrips
  are required for an upload.

- Make sure it doesn't end up with an enormous amount of nearly empty objects.

- When making a small change to the filetree, it needs to produce almost the same
  bit identical list of objects. Obviously the object holding the file you changed needs
  to change, and that will cause the objects that reference that object to change, but
  the more that the other objects do not change the better.

To do a good job at all the above concerns the algorithm needs to do some non-trivial work.
The biggest trick it has available is that a DirectoryNode message can either embed a Directory
message or refer to a Directory message stored in a different object. Similarly the Directory
message can embed a Leaves message or refer to a Leaves message in another object.

Most of the code is about deciding in a smart way when to go embedded, and when to go external.

For a small tree like this one:

```
README.md
src/helloworld.c
```

The algorithm would always decide to embed/inline, and you'd end up with these messages/objects:

Files:
- README.md -> in single object, let's call it f7e3a6a4d (abbreviated for clarity)
- helloworld.c -> in single object, let's call it a3f7e3a6a

```protobuf
Directory {
  Directories = [ 
    DirectoryNode {
      Name = "src",
      Contents_Internal = Directory {
        Directories = []
        Leaves_Internal = Leaves {
          Files = [ 
            FileNode {
              Name = "helloworld.c"
              Properties = FileProperties {
                Executable = false
                Contents = FileContents {
                  Reference = 1  //this will point to a3f7e3a6a
                }
              }
            } 
          ]               
        }
      }
    }
  ],
  Leaves_Internal = Leaves {
    Files = [
      FileNode {
        Name = "README.md"
        Properties = FileProperties {
          Executable = false
          Contents = FileContents {
            Reference = 2  //this will point to f7e3a6a4d
          }
        }
      }
    ]               
  }
}
```

That entire thing is one protobuf message. It will be written to its own object file, using
bonanza's object format where the targets of the references (a3f7e3a6a and f7e3a6a4d) will be written
at the beginning of the file. (the index numbers in the FileContents.Reference fields index into this)

This is an easy case where the algorithm only decided to inline. At some point always inlining would
result in objects that get bigger than desired, or even bigger than possible (2mb). Near the leaves of the
tree the inlined messages are still of reasonable size, but the closer you get to the root, the bigger they
would get. At some point the algorithm decides "this is enough, this thing is too big to inline". At that
point it will immediately decide to put that Leaves or Directory message into its own object. It will live
there by itself. It's okay for it to live there by itself because it was big. The reference to this newly
created object is calculated on the spot, and the Leaves_External or Contents_External will be made to point
at it.

The algorithm is recursive. When you ask it to calculate a merkle tree, you will end up getting back a Directory message.
It's produced like this:
- Produce a Leaves message for all immediate files & symlinks.
- Produce a Directory message for each immediate directory (by recursively calling into itself)

Then it starts building the Directory message that will end up being the result. Now we need to decide which
of the messages above we inline in the result, and which to externalize. We make a list of candidates to inline and call
`/pkg/model/core/inlinedtree.Build`. That function uses a whole set of heuristics I won't duplicate here, but its
only job is for each candidate decide to inline or externalize. It knows how to make the decision, but it doesn't
know how to act on it. Therefore you have to pass in a function that will either assign Leaves_Internal
or Leaves_External. This function is called a ParentAppender.

When the decision is to externalize the inlinedtree.Build function takes responsibility for creating a new object.
It passes this object to the parent appender, which interprets that as "oh looks like it decided to make a new object,
so I will assign `leaves_external` to that object".

When it decides to inline it will not pass an external object to the function, which you should interpret as "ok, it wants me to inline it",
and then the function will set `leaves_inine`. (same logic, but different function implementation for `DirectoryNode.contents_inline` vs
`DirectoryNode.contents_external`)

It aims for 16-64kb for objects holding directory information, and 64-256kb for objects holding files.

This inlining/externalizing process happens at every level of the filetree.

At the end of the day the original caller just gets a Directory message back, and can get to work.

Interesting observations:
- Objects that hold the files will be relatively stable. If the file doesn't change much, the objects that file is spread over don't change much. The closer you get to the root of a tree,
say the linux kernel tree, the more the objects will churn. Any change in a file will cause the object that holds the Directory
that references it to also change, and the object that holds the Directory that points to that also change.

- You can only reference an object as a whole. There's no way to index into it. Therefore the only things, in the context of this filesystem work, you can store in
  objects are:
     - single protobuf (like Leaves)  
     - single array of protobufs (like array of FileContents)
     - raw bytes for a file

Let's take a closer look at how files are dealt with. Above I brushed over it when I wrote that "you make a Leaves message for the files and symlinks".
When the algorithm above encounters a file that's not very big, then it's easy: it will make a single object for the file, 
and the Leaves message will be populated with a FileContents message that points to that object.

When the file is big, things get more interesting. It will be cut up into small pieces. Each piece goes into its own object,
and there will be another "indirection" object that holds an array of FileContents messages that point to all the segment objects.
Finally the Leaves message will have a FileContents message that points to the indirection object.

The art is in deciding how to chop it up. If you chop it up naively, say in 256kb segments, a tiny change in the beginning
of the file, will cause all objects to change. Turns out there's smarter ways to cut it up. If you somehow would be able to
find the same cut points as last time, even if there have been some changes here and there, many segments could remain identical.
We use a trick where we look at a sliding window of 64 bytes at a time, generate a deterministic random number from them (by hashing them), and then
saying "if the number is high, this is a good cut point". It's completely arbitrary, but that's okay, as long as next time you
use the same heuristic to find cut points, you are likely to find the same cut points as last time, and you'll get many identical
segment objects, even in the face of changes having occurred in the file.


When the file is really big, you can imagine wanting more than one indirection object. Let's say you have 300 file segments objects
you need to refer to. You could have an indirection object have 200 FileContents messages that point to file segments,
and then one final one that points to another indirection object that contains the remaining 100 file segments.
You could also have two indirection objects with 150 FileContents pointing to file objects each, and then one object that
has two FileContents messages pointing to each of those.

Just like with the job of splitting up a big file, here there's also naive ways to arrange this tree of FileContents
messages, and smarter ones. Bonanza uses a smarter one. It uses a prollyTree (`/pkg/model/core/btree/prolly_tree`) [https://docs.dolthub.com/architecture/storage-engine/prolly-tree](https://docs.dolthub.com/architecture/storage-engine/prolly-tree). 
Imagine our 300 FileContents messages in one big list. We need to decide where to "make a cut". The nodes we cut we'll put
in a new object, and where we cut them from, we'll replace with a FileContents message that points to the new file.
So in the file case, we had to cut at the granularity of bytes, here we cut at the granularity of FileContents messages.

The prolly tree uses a similar content based hashing technique. It hashes the individual messages, and the higher the number,
the more we want to use it as a cut point. Due to the other properties of a prolly tree, this makes it likely that
even when a file changes, which inevitably causes the FileContents message that points to the segment containing the change to change,
that other "groups of FileContents messages" that happened to point to file segments that didn't change, also don't change.

It might feel like a bit of overkill to add all this complexity of the prollyTree to the story of how we store files, but
the prollytree is used for dozens of other scenarios in bonanza, and it only feels natural to also use it for distributing
FileContents messages over objects, since we have the implementation laying around anyway.

## About merkle tree construction code

When you have a protobuf that has a reference to another object, that reference is a core.Reference message.
It only contains a 1-based index into an array of full 320bit references that is written to the beginning of the object that
the protobuf lives in. This encoding gives us a challenge, because when we want to construct a reference to another object,
we do not yet know which index our outgoing reference will have. (they have to be sorted, other outgoing references might
be added later).

We deal with this problem by deferring the assignment of the index inside the core.Reference message.
We assign a temporary index of MaxInt. We store a pointer to the index inside the message so we can set it later.
We also remember the actual full 320bit reference of the outgoing reference, so it can later be written to the file.
This bookkeeping happens in ReferenceMessagePatcher. It has an AddReference() method, that takes a full 320bit outgoing
reference, and gives you back a core.Reference message that you can use, whose .index will later on be set for you by
the ReferenceMessagePatcher. This is the only way you should ever construct a core.Reference message.

The .AddReference() method also allows you to provide additional information about the outgoing reference.
Sometimes this is used to store the actual payload that the outgoing reference is pointing to. Sometimes this
is not used at all. Sometimes it's used to store some information that can be used to reconstruct the contents of the
object the outgoing reference points to. For instance a pathname.