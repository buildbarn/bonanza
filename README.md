# Bonanza: an experimental remote build system

Bonanza is an experimental build system that takes the remote execution
model introduced by Bazel to the extreme. Whereas Bazel only uses it to
run build actions (compilation actions, tests) remotely, Bonanza uses it
for **everything**. This means that on your local system you may have a
command line utility that does little more than upload (local changes
to) your source tree to a cluster, followed by issuing a request to kick
off a build there, and report any progress updates received from the
cluster. By using this model we attempt to achieve the following:

- **Improved decoupling from the local system.**

  Remote execution already allows Bazel to run actions on platforms that
  differ from what is used locally. For example, Bazel running on a Mac
  may schedule build actions running on a Linux system. However, Bazel
  is unable to do this for things like repository rules. This means that
  it's not always possible to make Bazel behave as if it's truly running
  on a different system. You see that people sometimes solve this by
  running Bazel inside of a Docker container, or are unable to perform
  certain actions locally, requiring them to do "CI driven development".
  This shouldn't be necessary.

- **Better performance under high network latency.**

  Bazel's remote execution protocol is inherently latency sensitive, due
  to the fact that an action can only be looked up or executed after its
  full set of input files is known. As actions may depend on each other,
  this leads to unnecessary delays when latency between Bazel and the
  remote execution cluster is high. By running the build remotely it is
  easier to run it on systems closer to storage and workers, thereby
  giving reasonable performance both from CI, at the office, and from
  home.

- **Reduction in local disk space usage.**

  For certain projects you see that Bazel's disk space usage is
  excessive. Even though it's possible to reduce the size of
  `bazel-out/` using flags like `--remote_download_minimal` and
  `--nobuild_runfile_links`, there is no way reduce the size of
  `external/`. Even for a relatively simple project like Buildbarn's
  own bb-storage, `external/` is 2.5 GB in size, which is 200 times as
  big as the Git checkout of that project.

  By performing all analysis remotely, none of this data needs to be
  present on the local system. A cluster may also cache this data
  centrally, which should lead to less time waiting on downloads and a
  reduction in network traffic against third-party sites.

- **Easier integration.**

  Systems that are capable of calling into Bazel (CI systems, web-based
  IDEs, etc.) often need to provide a full execution environments for
  running the Bazel CLI, so they frequently do things like launching
  Docker containers behind the scenes. In the case of Bonanza it is
  possible to launch builds by calling into a gRPC based service,
  meaning there is an opportunity to simplify the design of such
  systems.

- **Improved collaboration.**

  By running builds fully remotely, it should be easier to launch builds
  and share their progress and results with others. By having all source
  code associated with a given build present in storage, it should be
  easier for people to "clone" a build and collaborate on addressing
  build failures.

Whereas many new build systems make the mistake of designing their own
build language, Bonanza attempts to be compatible with Bazel as much as
realistically possible. It is therefore capable of parsing `BUILD.bazel`
files, reading rule definitions from ordinary `*.bzl` files, and
downloading modules from [Bazel Central Registry](https://registry.bazel.build)
that are declared in `MODULE.bazel`. Bonanza comes with a command line
utility named `bonanza_bazel`. This tool attempts to be a drop-in
replacement for the Bazel command line utility, accepting the same style
of command line flags and `.bazelrc` files.

## Status

Bonanza is at this point still highly experimental. However, it is
already capable of building all targets inside a slightly altered copy
of the bb-storage source tree. This means that Bonanza is already
complete enough that C++ compilation works (at least good enough to
build a functioning copy of `protoc`), and that Starlark rules such as
ones provided by bazel-gazelle, rules\_go, rules\_js, rules\_oci, and
rules\_python tend to work as expected.

Bonanza is currently unable to cache build results. This means that
every invocation of `bonanza_bazel` behaves as if a clean build is
performed. Work on addressing this limitation will start soon.

## Running Bonanza

This repository contains an example deployment of Bonanza's server side
components, which can be be spawned by running `bazel run
//deployments/demo`. Furthermore, the `bonanza_bazel` command line tool
can be built by running `bazel build //cmd/bonanza_bazel`.

After launching a cluster, it's worth reading the instructions on
[how to build bb-storage using Bonanza](docs/building_bb_storage.md), as
it gives a good overview of the differences between plain
Bazel/Buildbarn and Bonanza.

## Contributing to Bonanza

As this project essentially attempts to provide an alternative to Bazel,
it has a fairly large scope. Contributions are therefore very much
appreciated. Be sure to [join `#buildbarn` on Slack](https://github.com/buildbarn#join-us-on-slack)
to get involved.

## Additional resources

- [Presentation](https://docs.google.com/presentation/d/1uh6CxvvziQunw55e_bs1Juz3jfaiE-QJVs2DCfeMeTw/edit?usp=sharing) at the 2025-03-20 Snowflake Buildbarn meetup.
