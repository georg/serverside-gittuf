# serverside-gittuf

A standalone **git smart-HTTP server** built on [go-git v6](https://github.com/go-git/go-git)
that records a [gittuf](https://gittuf.dev) **Reference State Log (RSL)** entry for every
pushed ref change. The only interface is git itself:

- `git push` → the server appends a signed RSL entry at `refs/gittuf/reference-state-log`.
- `git fetch origin 'refs/gittuf/*:refs/gittuf/*'` → pull the RSL to verify what was written.

There is **no JSON/RPC API** — git is the whole surface.

It follows the *Storer model* from entiredb's `gittuf-storer-interface-spec`: the RSL logic
operates directly over go-git v6's `storage.Storer` plus an injected `Signer`, using go-git's
own commit codec (`object.Commit.Encode` / `EncodeWithoutSignature`). The RSL format and SSH
signing scheme match gittuf, so a gittuf client verifies the log — proven by a cross-verify
test that runs the real gittuf verifier against our output.

## Quick start

```sh
go build -o serverside-gittuf ./cmd/serverside-gittuf
./serverside-gittuf --addr 127.0.0.1:8080 --data-dir ./data
```

On first run it generates an ed25519 signing key at `./data/cluster_ed25519`, writes the
public half to `./data/cluster_ed25519.pub`, and logs it:

```
RSL public key (authorize this in gittuf policy to verify):
  ssh-ed25519 AAAAC3Nza... 
```

Then, from any working tree:

```sh
git init -b main && git commit --allow-empty -m "hello"
git remote add origin http://127.0.0.1:8080/myrepo     # repo auto-created on first push
git push origin main:refs/heads/main

# Pull the RSL and see what the server recorded:
git fetch origin 'refs/gittuf/*:refs/gittuf/*'
git log --format='%B' refs/gittuf/reference-state-log
# RSL Reference Entry
#
# ref: refs/heads/main
# targetID: <the commit you pushed>
# number: 1
```

Each RSL entry is an empty-tree commit SSH-signed (namespace `git`, SHA-512) by the server's
cluster key.

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `--addr` | `:8080` | listen address |
| `--data-dir` | `./data` | directory holding bare repositories |
| `--signing-key` | `<data-dir>/cluster_ed25519` | RSL signing key (load-or-generate) |

## Verifying the RSL with gittuf

The server signs; **wiring its key into a repo's trust is the repo admin's job**. To let a
gittuf client cryptographically verify the log, add the published public key as an authorized
principal in the repo's gittuf root of trust (`refs/gittuf/policy`) — which this server records
in the RSL like any other ref. Without that step you can still read the fetched RSL entries and
verify their SSH signatures against the published key directly.

Clients may also **co-manage** the RSL: push `refs/gittuf/reference-state-log` with your own
entries and the server adopts them, deduplicates its own entries against yours, and chains any
remainder on top.

## Layout

- `rsl/` — RSL format, append (advance-once-per-push), read, and signature verify over the
  go-git v6 `Storer` seam (the spec's Transactioner shape: object writes go through
  `Begin → tx.SetEncodedObject × N → tx.Commit`, one durable flush, with the ref CAS outside
  the transaction). In-process signing via `EncodeWithoutSignature` + a `Signer`.
- `txstore/` — adapts a `storage.Storer` that isn't a `storer.Transactioner` (the filesystem
  backend) into one that is, via an in-memory staging transaction that flushes on `Commit`.
  go-git's in-memory storage is already transactional and passes through unwrapped.
- `signer/` — SSH ed25519 signer (gittuf's SSHSIG scheme) + load-or-generate.
- `gitserver/` — smart-HTTP handlers. go-git does advertisement, packfile ingest, and
  upload-pack; we own the receive-pack ref-commit step so the RSL append happens before the
  push's `ok` report-status, and host the coexistence dedup.
- `cmd/serverside-gittuf/` — entrypoint.

## Limitations

- **No cross-store transaction.** Unlike a Postgres-backed host, a filesystem backend has no
  rollback. The per-repo lock serializes pushes and the buffered report-status means `ok` is
  returned only after both the ref and its RSL entry are durable; if the RSL append fails after
  the ref is applied, the push reports failure and a client retry heals it (re-applies the
  no-op ref and writes the entry — no duplicate). A client-pushed RSL tip is validated before
  it is adopted, so a malformed log can't park the RSL ref on a corrupt commit.
- **Protocol v0/v1 only** (go-git's v2 server support is incomplete); clients fall back.
- **Force-push semantics** (go-git applies pushes via `SetReference`, not a client-old CAS).
- **Unauthenticated, plain HTTP, single-node.** Front it with TLS/auth/a reverse proxy for
  anything beyond a trusted network. Auto-init creates a repo on first push to any valid name.
- go-git v6 is alpha; pinned to a specific version.

## Tests

```sh
go test ./...
```

Covers: RSL unit behavior (numbering, parent-chaining, exact message bytes, fail-closed tip
read, signature verify), an end-to-end push+fetch over the go-git client, coexistence dedup and
malformed-RSL rejection, a real-`git`-CLI interop test, and a **gittuf cross-verify** test that
opens the produced RSL with the real gittuf library and verifies every entry (sha1; needs the
`git` binary — both skip if absent).
