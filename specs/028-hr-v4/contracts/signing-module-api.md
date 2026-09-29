# Contract: signing module API for HR (`signing.v1.ModuleSubmissions`)

New SDK module `github.com/go-tangra/go-tangra-signing/sdk/v4`, proto
`sdk/api/proto/signing/v1/module.proto`, generated Go in `sdk/gen/…`, client helper
`sdk/pkg/signingclient`. Served on signing's gRPC port (9915) over SPIFFE mTLS.
Allowed callers: `svc/hr` only (signing `deploy/policy.yaml` rule `hr-module-api`).
Research D1.

```proto
syntax = "proto3";
package signing.v1;

service ModuleSubmissions {
  rpc ListTemplates(ListTemplatesRequest) returns (ListTemplatesResponse);
  rpc CreateAndSend(CreateAndSendRequest) returns (CreateAndSendResponse);
  rpc GetSubmission(GetSubmissionRequest) returns (GetSubmissionResponse);
  rpc Cancel(CancelRequest) returns (CancelResponse);
  rpc Delete(DeleteRequest) returns (DeleteResponse);
  rpc FinalDocument(FinalDocumentRequest) returns (stream DocumentChunk);
}

message ListTemplatesRequest { string tenant_id = 1; }            // active templates only
message TemplateParty { string id = 1; string name = 2; }
message TemplateField { string id = 1; string name = 2; string type = 3; string party = 4; bool text_valued = 5; }
message TemplateInfo  { string id = 1; string name = 2; repeated TemplateParty parties = 3; repeated TemplateField fields = 4; }
message ListTemplatesResponse { repeated TemplateInfo templates = 1; }  // ≤ 500

message SignerRef { string user_id = 1; string party = 2; }
message CreateAndSendRequest {
  string tenant_id = 1;
  string template_id = 2;
  string sender_user_id = 3;          // the approving user (created_by); active member
  string name = 4;                    // ≤ 200, e.g. "Annual leave — 03.08.2026–07.08.2026"
  repeated SignerRef signers = 5;     // sequential in this order; 1–10
  map<string,string> prefill = 6;     // template field id → value (validated as the browser path)
  string source_ref = 7;              // hr request id (uuid)
  string idempotency_key = 8;         // hr request id + attempt; replays return the same submission
}
message CreateAndSendResponse { string submission_id = 1; string status = 2; }

message GetSubmissionRequest  { string tenant_id = 1; string submission_id = 2; }
message GetSubmissionResponse {
  string submission_id = 1;
  string status = 2;                  // in_progress | completed | cancelled | expired
  string cancel_reason_code = 3;      // cancelled | declined ("" otherwise)
  int32  final_version = 4;
}

message CancelRequest  { string tenant_id = 1; string submission_id = 2; string reason_code = 3; } // hr_cancelled | hr_rejected | member_left
message CancelResponse { string status = 1; }
message DeleteRequest  { string tenant_id = 1; string submission_id = 2; }
message DeleteResponse {}

message FinalDocumentRequest { string tenant_id = 1; string submission_id = 2; }
message DocumentChunk { bytes data = 1; string file_name = 2; int64 size = 3; } // 64 KiB chunks; metadata in the first
```

## Rules enforced by signing

- Caller = mTLS peer SPIFFE id; only `svc/hr` passes the policy; the handler also
  rejects any other service (defence in depth). Subject: service actor for
  `tenant_id` (UUID) — never a platform admin.
- `ListTemplates`: the tenant's `active` templates; no PDF, no prefill values.
- `CreateAndSend`: template active and in the tenant; every signer and the sender are
  active tenant members (auth `Contacts`); signers' parties exist and are distinct;
  mode sequential; prefill validated (`InvalidPrefill`); stores `source = 'hr'`,
  `source_ref`, `created_by = sender_user_id`; creates and sends in one call (on send
  failure the draft is deleted); idempotent by `idempotency_key` (unique per tenant
  and source).
- `GetSubmission`, `Cancel`, `Delete`, `FinalDocument`: only submissions with
  `source = 'hr'` of the tenant; otherwise `NotFound`. `FinalDocument` only when
  `completed` (`FailedPrecondition` otherwise).
- Audit: `submission.create/send/cancel/delete` with actor = `svc/hr`, sender recorded;
  signing history `submission.created` meta `{source: hr}`.
- gRPC codes: InvalidArgument (validation, field in details), NotFound,
  FailedPrecondition (template_not_active, signer_inactive, not_completed),
  PermissionDenied (wrong caller), Unavailable.

## Schema change in signing (`0004_source.sql`)

```sql
ALTER TABLE signing_submissions
  ADD COLUMN source text NOT NULL DEFAULT '' CHECK (source IN ('', 'hr')),
  ADD COLUMN source_ref text NOT NULL DEFAULT '' CHECK (char_length(source_ref) <= 64),
  ADD COLUMN idempotency_key text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX signing_submissions_idem ON signing_submissions (tenant_id, source, idempotency_key)
  WHERE idempotency_key <> '';
```

The browser UI shows "Source: HR" on such submissions; their sender (the approver)
can follow them as usual. Events are unchanged (F7).
