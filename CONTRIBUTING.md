### Set up your mc GitHub Repository
Fork the [pgsty/mc](https://github.com/pgsty/mc/fork) repository to your own personal repository.
```
$ git clone https://github.com/$USER_ID/mc
$ cd mc
$ make
$ ./mc --help
```

###  Developer Guidelines

``mc`` welcomes your contribution. To make the process as seamless as possible, we ask for the following:

* Go ahead and fork the project and make your changes. We encourage pull requests to discuss code changes.
    - Fork it
    - Create your feature branch (git checkout -b my-new-feature)
    - Commit your changes with a DCO sign-off (git commit -s -am 'Add some feature')
    - Push to the branch (git push origin my-new-feature)
    - Create new Pull Request against the `main` branch of [pgsty/mc](https://github.com/pgsty/mc)

* If you have additional dependencies for ``mc``, ``mc`` manages its dependencies using `go mod`
    - Run `go get foo/bar`
    - Edit your code to import foo/bar
    - Run `go mod tidy` from top-level folder

* When you're ready to create a pull request, be sure to:
    - Have test cases for the new code. If you have questions about how to do it, please ask in your pull request.
    - Run `go fmt`
    - Squash your commits into a single commit. `git rebase -i`. It's okay to force update your pull request.
    - Make sure `make install` completes.

* Read [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments) from the Go project
    - `mc` project is conformant with Golang style
    - if you happen to observe offending code, please feel free to send a pull request

### Licensing of Contributions

This project is licensed under the [GNU AGPL v3.0 or later](LICENSE). Its core
is Copyright (c) MinIO, Inc.; the combined work can never be relicensed, and
this fork does not try to.

* **No CLA.** We do not ask you to sign a Contributor License Agreement and we
  do not take your copyright. Contributions are accepted inbound=outbound: you
  keep the copyright to your changes and license them under the same
  AGPL-3.0-or-later as the project itself. The maintainers receive no rights
  beyond the project license.

* **DCO sign-off required.** Every commit must carry a
  `Signed-off-by: Your Name <you@example.com>` trailer certifying the
  [Developer Certificate of Origin 1.1](https://developercertificate.org/) —
  your statement that you have the right to submit the code under the project
  license. Sign each commit with:

  ```
  git commit -s
  ```

  Forgot some? Repair your branch with `git rebase --signoff` and force-push.
  CI rejects pull requests containing unsigned commits; the sign-off email
  must match the commit author email. (Lowercase `-s` is the plain-text DCO
  sign-off; cryptographic `-S`/GPG signing is welcome but independent.)

* **Provenance.** Only submit code you are entitled to submit. When relaying a
  patch written by someone else — for example cherry-picking from the archived
  upstream or another fork — preserve original authorship
  (`git cherry-pick -x`, keep the author field and any existing
  `Signed-off-by` trailers) and add your own sign-off as the person passing it
  along.

* **File headers.** Files derived from upstream keep the original MinIO
  copyright header unchanged. New files added by this fork use the dual
  header, followed by the standard AGPL boilerplate:

  ```
  // Copyright (c) 2015-2025 MinIO, Inc.
  // Copyright (c) 2025-2026 PGSTY
  ```

* **Squash merges** must keep the `Signed-off-by:` trailers in the resulting
  commit message.
