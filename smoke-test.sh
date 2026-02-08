#!/bin/sh
# Phase 5 Smoke Tests for primal-pds
# Run inside the container: docker exec primal-pds sh /app/smoke-test.sh
# Or from host: docker cp smoke-test.sh primal-pds:/tmp/ && docker exec primal-pds sh /tmp/smoke-test.sh
#
# Requires curl (apk add curl) — busybox wget drops response bodies on non-2xx.

set -e

BASE="http://localhost:3000"
ADMIN="test-admin-key"
PASS=0
FAIL=0
TOTAL=0

# Install curl if missing
if ! command -v curl >/dev/null 2>&1; then
  apk add --no-cache curl >/dev/null 2>&1
fi

ok() { TOTAL=$((TOTAL+1)); PASS=$((PASS+1)); echo "  PASS [$TOTAL] $1"; }
fail() { TOTAL=$((TOTAL+1)); FAIL=$((FAIL+1)); echo "  FAIL [$TOTAL] $1: $2"; }

xget() {
  if [ -n "$2" ]; then
    curl -sf -H "$2" "$1" 2>/dev/null || curl -s -H "$2" "$1" 2>/dev/null
  else
    curl -sf "$1" 2>/dev/null || curl -s "$1" 2>/dev/null
  fi
}

xpost() {
  if [ -n "$3" ]; then
    curl -sf -X POST -H "Content-Type: application/json" -H "$3" -d "$2" "$1" 2>/dev/null \
      || curl -s -X POST -H "Content-Type: application/json" -H "$3" -d "$2" "$1" 2>/dev/null
  else
    curl -sf -X POST -H "Content-Type: application/json" -d "$2" "$1" 2>/dev/null \
      || curl -s -X POST -H "Content-Type: application/json" -d "$2" "$1" 2>/dev/null
  fi
}

xpost_raw() {
  curl -sf -X POST -H "$3" -H "$4" --data-binary "$2" "$1" 2>/dev/null \
    || curl -s -X POST -H "$3" -H "$4" --data-binary "$2" "$1" 2>/dev/null
}

echo "=== Phase 5 Smoke Tests ==="
echo ""

# --- Health & Discovery ---
echo "--- Health & Discovery ---"
R=$(xget "$BASE/xrpc/_health")
echo "$R" | grep -q '"version":"0.6.0"' && ok "health returns 0.6.0" || fail "health" "$R"

R=$(xget "$BASE/xrpc/com.atproto.server.describeServer")
echo "$R" | grep -q '"availableUserDomains"' && ok "describeServer returns availableUserDomains" || fail "describeServer" "$R"
echo "$R" | grep -q '".test.local"' && ok "describeServer includes .test.local" || fail "describeServer domain" "$R"

# --- Account Creation ---
echo ""
echo "--- Account Creation ---"
R=$(xpost "$BASE/xrpc/host.primal.pds.createAccount" \
  '{"domain":"test.local","handle":"smoketest","password":"testpass123"}' \
  "Authorization: Bearer $ADMIN")
echo "$R" | grep -q '"did"' && ok "createAccount (admin API)" || fail "createAccount" "$R"
SMOKE_DID=$(echo "$R" | sed 's/.*"did":"\([^"]*\)".*/\1/')
echo "   -> DID: $SMOKE_DID"

# --- Session Auth ---
echo ""
echo "--- Session Auth ---"
R=$(xpost "$BASE/xrpc/com.atproto.server.createSession" \
  '{"identifier":"smoketest.test.local","password":"testpass123"}')
echo "$R" | grep -q '"accessJwt"' && ok "createSession returns accessJwt" || fail "createSession" "$R"
echo "$R" | grep -q '"refreshJwt"' && ok "createSession returns refreshJwt" || fail "createSession refreshJwt" "$R"
echo "$R" | grep -q "$SMOKE_DID" && ok "createSession returns correct DID" || fail "createSession DID" "$R"
ACCESS=$(echo "$R" | sed 's/.*"accessJwt":"\([^"]*\)".*/\1/')
REFRESH=$(echo "$R" | sed 's/.*"refreshJwt":"\([^"]*\)".*/\1/')

R=$(xget "$BASE/xrpc/com.atproto.server.getSession" "Authorization: Bearer $ACCESS")
echo "$R" | grep -q "$SMOKE_DID" && ok "getSession returns correct DID" || fail "getSession" "$R"
echo "$R" | grep -q '"handle":"smoketest.test.local"' && ok "getSession returns correct handle" || fail "getSession handle" "$R"
echo "$R" | grep -q '"didDoc"' && ok "getSession includes DID document" || fail "getSession didDoc" "$R"

R=$(xpost "$BASE/xrpc/com.atproto.server.refreshSession" '{}' "Authorization: Bearer $REFRESH")
echo "$R" | grep -q '"accessJwt"' && ok "refreshSession returns new accessJwt" || fail "refreshSession" "$R"
NEW_ACCESS=$(echo "$R" | sed 's/.*"accessJwt":"\([^"]*\)".*/\1/')

R=$(xpost "$BASE/xrpc/com.atproto.server.createSession" \
  "{\"identifier\":\"$SMOKE_DID\",\"password\":\"testpass123\"}")
echo "$R" | grep -q '"accessJwt"' && ok "createSession with DID identifier" || fail "createSession DID login" "$R"

R=$(xpost "$BASE/xrpc/com.atproto.server.createSession" \
  '{"identifier":"smoketest.test.local","password":"wrongpass"}')
echo "$R" | grep -q '"AuthenticationRequired"' && ok "createSession rejects wrong password" || fail "createSession wrong pass" "$R"

R=$(xpost "$BASE/xrpc/com.atproto.server.deleteSession" '{}' "Authorization: Bearer $REFRESH")
ok "deleteSession (no-op, no error)"

# --- Repo Auth ---
echo ""
echo "--- Repo Auth ---"
R=$(xpost "$BASE/xrpc/com.atproto.repo.createRecord" \
  "{\"repo\":\"$SMOKE_DID\",\"collection\":\"app.bsky.feed.post\",\"record\":{\"\$type\":\"app.bsky.feed.post\",\"text\":\"smoke test post\",\"createdAt\":\"2026-02-08T00:00:00Z\"}}" \
  "Authorization: Bearer $NEW_ACCESS")
echo "$R" | grep -q '"uri"' && ok "JWT createRecord own repo" || fail "JWT createRecord" "$R"
POST_URI=$(echo "$R" | sed 's/.*"uri":"\([^"]*\)".*/\1/')
POST_RKEY=$(echo "$POST_URI" | sed 's|.*/||')

# Get the owner DID for wrong-repo test
OWNER_DID=$(xget "$BASE/xrpc/host.primal.pds.listAccounts?domain=test.local" "Authorization: Bearer $ADMIN" \
  | sed 's/.*"did":"\([^"]*\)".*"role":"owner".*/\1/')
R=$(xpost "$BASE/xrpc/com.atproto.repo.createRecord" \
  "{\"repo\":\"$OWNER_DID\",\"collection\":\"app.bsky.feed.post\",\"record\":{\"\$type\":\"app.bsky.feed.post\",\"text\":\"hack\",\"createdAt\":\"2026-02-08T00:00:00Z\"}}" \
  "Authorization: Bearer $NEW_ACCESS")
echo "$R" | grep -q '"Forbidden"' && ok "JWT createRecord wrong repo -> 403" || fail "JWT wrong repo" "$R"

R=$(xpost "$BASE/xrpc/com.atproto.repo.createRecord" \
  "{\"repo\":\"$SMOKE_DID\",\"collection\":\"app.bsky.feed.post\",\"record\":{\"\$type\":\"app.bsky.feed.post\",\"text\":\"admin post\",\"createdAt\":\"2026-02-08T00:00:01Z\"}}" \
  "Authorization: Bearer $ADMIN")
echo "$R" | grep -q '"uri"' && ok "admin createRecord any repo" || fail "admin createRecord" "$R"

# --- Public Reads ---
echo ""
echo "--- Public Reads ---"
R=$(xget "$BASE/xrpc/com.atproto.repo.getRecord?repo=$SMOKE_DID&collection=app.bsky.feed.post&rkey=$POST_RKEY")
echo "$R" | grep -q '"smoke test post"' && ok "getRecord public (no auth)" || fail "getRecord public" "$R"

R=$(xget "$BASE/xrpc/com.atproto.repo.listRecords?repo=$SMOKE_DID&collection=app.bsky.feed.post")
echo "$R" | grep -q '"records"' && ok "listRecords public (no auth)" || fail "listRecords public" "$R"

R=$(xget "$BASE/xrpc/com.atproto.repo.describeRepo?repo=$SMOKE_DID")
echo "$R" | grep -q '"collections"' && ok "describeRepo public (no auth)" || fail "describeRepo public" "$R"

# --- Identity ---
echo ""
echo "--- Identity ---"
R=$(xget "$BASE/xrpc/com.atproto.identity.resolveHandle?handle=smoketest.test.local")
echo "$R" | grep -q "$SMOKE_DID" && ok "resolveHandle returns correct DID" || fail "resolveHandle" "$R"

# --- Sync ---
echo ""
echo "--- Sync ---"
R=$(xget "$BASE/xrpc/com.atproto.sync.getLatestCommit?did=$SMOKE_DID")
echo "$R" | grep -q '"cid"' && ok "getLatestCommit returns cid" || fail "getLatestCommit" "$R"
echo "$R" | grep -q '"rev"' && ok "getLatestCommit returns rev" || fail "getLatestCommit rev" "$R"

R=$(xpost "$BASE/xrpc/com.atproto.sync.requestCrawl" '{"hostname":"test.local"}')
ok "requestCrawl accepted"

# --- Blobs ---
echo ""
echo "--- Blobs ---"
R=$(xpost_raw "$BASE/xrpc/com.atproto.repo.uploadBlob?did=$SMOKE_DID" \
  "fakepngdata12345" \
  "Authorization: Bearer $NEW_ACCESS" \
  "Content-Type: image/png")
echo "$R" | grep -q '"blob"' && ok "uploadBlob returns blob ref" || fail "uploadBlob" "$R"
BLOB_CID=$(echo "$R" | sed 's/.*"\$link":"\([^"]*\)".*/\1/')
echo "   -> CID: $BLOB_CID"

if [ -n "$BLOB_CID" ] && [ "$BLOB_CID" != "$R" ]; then
  R=$(xget "$BASE/xrpc/com.atproto.sync.getBlob?did=$SMOKE_DID&cid=$BLOB_CID")
  echo "$R" | grep -q "fakepngdata" && ok "getBlob returns data" || fail "getBlob" "$R"
else
  fail "getBlob" "no CID from upload"
fi

# --- Registration Gate ---
echo ""
echo "--- Registration Gate ---"
R=$(xpost "$BASE/xrpc/com.atproto.server.createAccount" \
  '{"handle":"public.test.local","password":"pass123"}')
echo "$R" | grep -q '"AuthRequired"\|"RegistrationClosed"' && ok "createAccount XRPC blocked (registration closed)" || fail "createAccount gate" "$R"

# --- Cleanup ---
echo ""
echo "--- Cleanup ---"
R=$(xpost "$BASE/xrpc/host.primal.pds.deleteAccount" '{"handle":"smoketest.test.local"}' "Authorization: Bearer $ADMIN")
echo "$R" | grep -q "Account deleted" && ok "cleanup: delete smoketest account" || fail "cleanup" "$R"

# --- Summary ---
echo ""
echo "=== Results: $PASS/$TOTAL passed, $FAIL failed ==="
[ $FAIL -eq 0 ] && exit 0 || exit 1
