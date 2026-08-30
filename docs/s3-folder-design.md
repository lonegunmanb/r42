# S3-Compatible Folder Upload Design

## Status

Implementation contract for the `s3_provider`, `s3_folder`, research `path`,
and upload rollback behavior.

## Goals

- Upload every eligible regular file under a local directory to an S3-compatible
  bucket using deterministic object keys.
- Support AWS S3 and Alibaba Cloud OSS through their S3-compatible APIs.
- Make a folder upload a normal DAG node so references and `depends_on` control
  ordering.
- Stop at the first failed object upload and roll back objects uploaded by this
  block in reverse order.
- Use bucket versioning when available so rollback can delete the exact
  versions created by this block without destroying an older object version.
- Keep credentials out of ordinary plan display, progress output, and errors.

Non-goals for the first implementation are cross-block conflict detection,
remote deletion of objects that predate this block, and uploading arbitrary
paths outside the active run directory.

## HCL Shape

```hcl
s3_provider "oss" {
  endpoint          = "https://oss-cn-hangzhou.aliyuncs.com"
  region            = "cn-hangzhou"
  access_key_ref    = "ALIBABA_CLOUD_ACCESS_KEY_ID"
  secret_key_ref    = "ALIBABA_CLOUD_ACCESS_KEY_SECRET"
  session_token_ref = "ALIBABA_CLOUD_SECURITY_TOKEN"
  force_path_style  = false

  retry {
    max_retries          = 5
    interval_seconds     = 2
    max_interval_seconds = 30
  }
}

research "static" "market" {
  # ...
}

s3_folder "market_result" {
  provider = s3_provider.oss
  bucket   = "research-results"
  source   = research.static.market.path
  prefix   = "market/2026-08-28"
  exclude  = ["events.jsonl", "**/*.tmp"]
}
```

`s3_provider` has one label and is configuration-only. `s3_folder` has one
label and is executable. Both are root-level declarations and may use
`for_each` where the existing Golden block rules permit it. Provider blocks do
not become Apply nodes; each planned folder snapshot retains the referenced
provider configuration needed at Apply.

### `s3_provider` fields

| Field | Required | Meaning |
| --- | --- | --- |
| `endpoint` | No | Custom HTTPS endpoint. Omitted means the AWS SDK default endpoint. OSS uses its regional endpoint. |
| `region` | Yes | Signing region. AWS and OSS deployments must provide the region used by the endpoint. |
| `access_key` / `access_key_ref` | One of each pair, optional | Static access key or environment variable name. At most one may be set. Ref values are resolved only during Apply. |
| `secret_key` / `secret_key_ref` | One of each pair, optional | Static secret key or environment variable name. At most one may be set. Ref values are resolved only during Apply. |
| `session_token` / `session_token_ref` | One of each pair, optional | Optional STS/security token. |
| `force_path_style` | No | Use path-style addressing. Defaults to `false`; OSS deployments may select the setting required by their endpoint. |
| `retry` | No | S3 upload retry policy. At most one nested block. |

The provider must also accept an explicit AWS/OSS-compatible endpoint and must
not assume that an AWS hostname is present. Literal credentials are sensitive
configuration; refs are the recommended form. No credential value is included
in a result, lifecycle event, progress frame, or error string.

The S3 retry policy is independent from the model provider retry policy:

| Field | Meaning |
| --- | --- |
| `max_retries` | Number of additional attempts after the initial attempt. `0` means one attempt. |
| `interval_seconds` | Initial backoff delay. |
| `max_interval_seconds` | Backoff ceiling. |
| `error_message_regex` | Optional additional transient-error matchers. |

The implementation must disable or replace the SDK's implicit retry loop so a
file is not retried twice by two independent policies. Retriable failures are
network timeouts/resets, HTTP 408, 429, and 5xx responses. Authentication,
authorization, not-found, invalid-request, and context cancellation failures
are permanent.

### `s3_folder` fields

| Field | Required | Meaning |
| --- | --- | --- |
| `provider` | Yes | Reference to one `s3_provider` block. The reference creates a normal dependency on configuration. |
| `bucket` | Yes | Destination bucket name. |
| `source` | Yes | Local directory. Relative paths are below the active run root; absolute paths must also remain below it after cleaning. A research `path` reference is the normal source form. |
| `prefix` | No | Object key prefix. Empty is allowed. It must not start or end with `/`, contain empty segments, `.` or `..` segments, backslashes, or control characters. |
| `exclude` | No | Glob patterns relative to source root. `**` matches zero or more directory components and may match directories. |
| `retry` | No | Optional folder-level override layered over provider retry policy. At most one nested block. |

The block publishes a plan-known shape before Apply with no object list. After a
successful Apply, its result contains only stable summary data:

```hcl
s3_folder.market_result.result = {
  bucket        = "research-results"
  prefix        = "market/2026-08-28"
  root          = "s3://research-results/market/2026-08-28"
  object_count  = 12
}
```

`object_keys` and `bytes_uploaded` are intentionally not part of the result.
An empty file set after excludes is a successful no-op with `object_count = 0`.

## Research `path`

Both research variants expose a plan-known `path` attribute:

- `research.static.<name>.path` is the static block workspace.
- `research.dynamic.<name>.path` is the dynamic parent workspace containing its
  materialized task directories.

The path is absolute and uses `/` separators in HCL values. A reference to it
creates the normal implicit DAG dependency. A dynamic task does not expose an
independent path in this version.

`path.module` is the configuration source directory, not the run directory. It
is therefore not a valid `s3_folder.source` under the run-root confinement rule;
use a research `path` or a path below the run root instead.

## Source Resolution and Walk

Source resolution is Apply-time because a block workspace may not exist until
the producing block runs. The run root is the directory returned by the active
`run.Run`, for example `.r42/runs/run-<id>`.

1. Resolve a relative source against the run root. Convert an absolute source
   to an absolute, cleaned path.
2. Reject an empty source, a non-directory, a root symlink, or a cleaned path
   outside the run root. Boundary checking uses `filepath.Rel`; equality with
   the root is allowed, while `..` or a `..`-prefixed relative result is not.
   Do not use a plain string prefix check.
3. Walk the directory without following symlinks. Skip symlink entries and
   special files. Hidden files and directories are included unless excluded.
4. Normalize each relative path to `/`, apply `exclude` patterns, sort the
   remaining files lexicographically, and derive the object key as
   `prefix + "/" + relative-path` (or just the relative path for an empty
   prefix).

The path-component check follows go-getter v2's safety model: slash means both
`/` and `\\`, and `..` is recognized only as an entire component. This check is
performed before and after filesystem normalization; `filepath.Rel` remains the
authoritative containment test for the host platform.

## Apply and Rollback

Before the first PUT, the block checks bucket versioning. `Enabled` permits
safe version-specific rollback. A disabled or suspended bucket remains valid,
but the block records that rollback is unavailable; deleting an unversioned
overwritten key could destroy an older object that this block does not own.

Each regular file is opened as a stream. Small files may use a single PUT;
larger files may use multipart upload. Multipart failures must abort the
in-progress upload before rollback proceeds. The exact version identifier
returned by a successful PUT or completed multipart upload is recorded with the
object key.

The first failed file stops the walk and no later file is attempted. The block
then deletes recorded object versions in reverse upload order. Cleanup uses a
bounded context independent of the canceled Apply context. A cleanup failure,
including a failed multipart abort, is part of the returned error.

The returned error includes:

- the local source root;
- the remote root `s3://<bucket>/<prefix>`;
- the failed object key;
- the primary upload error; and
- any rollback or multipart-abort errors.

The block has failed even if rollback succeeds. If rollback fails, or if the
bucket is unversioned/suspended and therefore cannot be rolled back safely, the
remote root gives an administrator a manual cleanup target. Old versions are
never deleted by rollback.

## Progress, Logging, and Plan Persistence

`s3_folder` participates in the existing block lifecycle and progress stream.
Progress records may contain the block address, bucket, prefix, object count,
and sanitized error summaries, but never credentials, object contents, or full
SDK request payloads. Debug mode may persist existing sensitive events, so
uploading a research directory that contains `events.jsonl` remains an explicit
user responsibility.

The saved Plan stores provider and folder configuration needed for Apply. Ref
names, not their resolved environment values, are persisted. Literal sensitive
values retain the repository's sensitive-mark behavior and must be excluded
from human-readable plan output.

## Explicit Non-Goals

- No automatic serialization between two `s3_folder` blocks targeting the same
  key. Use `depends_on` when ordering is required.
- No remote listing or preflight conflict scan beyond versioning status.
- No restoration of overwritten object data.
- No symlink dereferencing or special-file upload.
- No upload of the configuration/module source directory outside the run root.
