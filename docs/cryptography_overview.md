# Bonanza Cryptographic Design

Bonanza is designed to be useful even in situations where not all users trust all parts of the infrastructure. To do so it uses cryptography in several places that might not immediately feel obvious. This document is my (Lucas Meijer)'s attempt to describe how all of this works from a "you might not be a crypto expert" perspective.

## Storage Layer

The storage layer itself is oblivious of encryption - users can add encryption on top through a configurable stack of encoders. Before objects get written to storage, they go through these encoders. A typical setup would first compress, then encrypt. Only the payload of the objects get encrypted. The header that contains the 40-byte references that point to other objects are unencrypted. These references contain SHA256 content hashes of the encoded objects they point to. This allows the storage layer to do things like garbage collection, or quickly being able to answer questions like "is object X and everything it recursively points to still in storage".

The encryption key is stored in the binary encoders configuration message. The initialization vector (IV) is stored in the parent object's encrypted payload for enhanced security. 

## Execution Layer

In the execution layer, things get more complicated:

### Routing Done by the Scheduler

When a worker node like bonanza_worker, or bonanza_fetch connects to the scheduler, it doesn't say "Hey! I can run Mac jobs, send them my way!". It advertises itself with a public key. When a client wants to send a job to this worker, it has to know that worker's public key. So a client needs to know "the public key all the Mac bonanza_workers in my cluster have been setup with", in order to get Mac jobs to actually execute. Notice that the Scheduler has no idea what kind of work the worker does. It does not know it's a Mac worker, it does not know it's bonanza_worker or bonanza_fetcher, or a custom worker type it has no idea even existed.

### Workers Only Accepting Jobs from Clients They Trust

We refer to "the thing that sends some action to the scheduler to get executed" as a client. bonanza_bazel sends action requests to bonanza_builder. bonanza_builder itself is a client when it sends actions to bonanza_worker's. Not all clients are allowed to send actions to workers of their choosing. The worker needs to be okay with that. 

The way this works is whoever configured the worker, configured it with a certificate authority's public key. This means "only accept jobs from clients that identify themselves with a certificate that was signed by this certificate authority. I'm providing you with the certificate authority's public key so you can verify if a client's certificate was signed by it or not".

### Client-Worker Communication Not Trusting the Scheduler

The client and the worker do not communicate directly. They talk through the scheduler. But they're designed to not have to trust the scheduler. So to prevent the scheduler from snooping on whatever work the client is asking the worker to do, they encrypt all of their communication. The bulk of the communication actually does not flow through the scheduler, but it flows through the storage layer. If bonanza_builder wants bonanza_worker to compile a C++ file, it will have put that C++ file in storage, put a directory contents message in storage that points to that C++ file. When the worker has compiled the C++ file into a .o file, the worker will have placed that .o file in storage for the client to read.

The communication that does flow through the scheduler is "hey, please execute the action I put into the storage at 0xdeadbeef for you" and the corresponding message on completion "hey, the work is done, you can find your results in storage at 0xbeefdada". It might feel like overkill to encrypt this communication, but it's not, because part of what's being communicated is not just the bonanza-reference that points into the storage, but also the decryption key to decrypt that data, as discussed in the [Storage Layer Encryption](#storage-layer-encryption) section.

This encryption uses [Elliptic-curve Diffie–Hellman](https://en.wikipedia.org/wiki/Elliptic-curve_Diffie%E2%80%93Hellman) where both the client and the worker are able to produce a shared secret by combining their own private key with the other's public key. To do so, only public keys have to be exchanged. This allows the two parties, and only these two parties, to generate the same shared secret. This shared secret is then used to encrypt/decrypt all communication that happens between a client and worker as part of requesting a job to be done, and reporting its results.

The shared secret actually gets massaged a little bit extra by XOR-ing the first bit with 1, 2 or 3, depending on the direction of the message. When this is required the .proto docs specify it explicitly. There's also a 12-byte random sequence called 'nonce' that gets thrown in the mix. The nonce and the bit flipping are done to make the crypto harder to crack.

### Client being protected against a rogue Worker.

A client wants to be sure that the worker that ends up doing the job the client asked for, is actually a worker that it trusts. Since
routing is done with a public key, a rogue worker could connect to the scheduler saying "hey, i'm a worker with this key too, send jobs my way!".
An adversarial worker like that would not be able to decrypt any of the client's request, nor would it be able to send back a result message. The worker doesn't have the private key required to generate the shared secret required to decrypt the clients request. This secret is also necessary to send a response back to the client that the client will be able to decrypt. There might still be a DDOS attack vector here, but no data leak one.

It's also worth noting that even though the client needs to ensure that the responses that it receives are actually created by a worker that it trusts, the scheduler also tries to help here. Any workers attempting to synchronize against the scheduler must also provide to the scheduler that they are in the possession of the platform's private key. This ensures that rogue workers can't hook into an arbitrary platform and steal work.


### Configuration Example

A program like bonanza_builder is both a worker (you can ask it to do a build for you), but also a client (it can ask a bonanza_worker to execute a job). It uses different keys for these roles. Because it is a worker it's configured with a:

- **platformPrivateKey**: used for routing. (The name platform feels very weird actually, maybe we should rename it to WorkerKindPrivateKey?) You don't have to provide the public key, because it can be generated from the private one. The public one that's used for routing, but you configure it with the private key, because that's used in the communication as described in [Client-Worker Communication](#client-worker-communication-not-trusting-the-scheduler))
- **clientCertificateAuthority**: the public-key-inside-certificate that has signed all trustworthy clients' public key certificates.

Because bonanza_builder also is a client, it is additionally configured with:
- **executionClientCertificateChain**: the public-key-certificate that was signed by the certificate authority that configured the bonanza_worker that bonanza_builder wants to ask to execute jobs.
- **executionClientPrivateKey**: The corresponding private key. Once the public key is accepted, the private key is used to encrypt the client-worker communication as described in [Client-Worker Communication](#client-worker-communication-not-trusting-the-scheduler).

## Reference Notes

### Cryptography Notes for Non-Experts

- All keys you have to provide in Bonanza configuration files are allowed to be X25519 keys, Ed25519 keys, or NIST curves.
- There are various practical limitations to embedding X25519 public keys in X.509 certificates. For example, it's not possible to create Certificate Signing Requests for them. OpenSSL is also unable to generate such certificates. This is why we also support Ed25519. However, Elliptic-curve Diffie–Hellman key agreement does not work directly with Ed25519 keys, as they use different mathematical operations (Edwards curves vs Montgomery curves). While you can mathematically derive an X25519 key from an Ed25519 key, this creates practical challenges for certificate generation workflows. Bonanza performs this conversion when provided an Ed25519 key and needs to generate a shared secret, but using X25519 keys directly is often more straightforward for this use case. 
- The strings you see with "-------- BEGIN PRIVATE KEY -------" are called PEM blocks. Most languages have support for parsing them. They can contain public keys, private keys, certificates and probably more. Bonanza configuration files take PEM blocks because it's convenient in a text editor.
- All crypto primitives also have raw binary representation.
- DER format is a binary representation that includes metadata about what it is that the bytes mean. For instance they can say "this is an Ed25519 private key". The Bonanza binary protocol typically uses DER encoding when it sends crypto primitives over the wire.