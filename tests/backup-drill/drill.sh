#!/bin/sh
# Restore drill: back up a populated Wrench to S3, lose the database and the local repository, restore
# from S3 alone into a fresh PostgreSQL and start Wrench on it. Run through `make backup-drill`.
set -eu
C="docker compose -f $(dirname "$0")/compose.yaml -p wrench-drill"
W=http://127.0.0.1:18085
G=http://127.0.0.1:19085

fail() { echo "backup drill: $*" >&2; exit 1; }
cleanup() { $C down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM
cleanup

$C build backup
$C up -d --build --wait postgres s3 gitlab wrench

# Sign in as user3 (an administrator: the drill reads /api/system) through the GitLab mock, as Pi would; the device
# key survives the restore with the database.
KEY=$(cd "$(dirname "$0")/.." && go run ./cmd/wrench-login -url "$W" -gitlab "$G" -email user3@example.com) ||
	fail "sign-in failed"
AUTH="Authorization: Bearer $KEY"

# Populate through the API and the journal.
$C exec -T postgres psql -U wrench -d wrench -q -v ON_ERROR_STOP=1 -c "
	INSERT INTO sessions (id, user_email, started_at, last_seen_at)
	SELECT 'drill-' || g, 'user3@example.com', now(), now() FROM generate_series(1, 3) g;
	INSERT INTO attempts (id, request_id, user_email, session_id, requested_model, started_at, finished_at,
		outcome, cost, cost_state)
	SELECT 'drill-' || g, 'r' || g, 'user3@example.com', 'drill-' || (g % 3 + 1), 'glm',
		now() - g * interval '1 minute', now() - g * interval '1 minute' + interval '2 seconds', 'success',
		0.000001 * g, 'confirmed'
	FROM generate_series(1, 500) g"

fingerprint() {
	$C exec -T postgres psql -U wrench -d wrench -At -c "
		SELECT (SELECT count(*) FROM sessions) || '/' ||
			(SELECT count(*) FROM attempts) || '/' ||
			(SELECT md5(string_agg(id || cost::text, ',' ORDER BY id)) FROM attempts)"
}
before=$(fingerprint)
echo "before: $before"

$C run --rm backup backup || fail "backup failed"
curl -fsS -H "$AUTH" "$W/api/system" | grep -q '"status":"ok"' || fail "Wrench does not report the backup as ok"

# Node loss: the database, the local repository and the status are gone; only S3 remains.
$C rm -fsv wrench backup postgres >/dev/null
docker volume rm wrench-drill_pgdata wrench-drill_backup_repo wrench-drill_backup_status >/dev/null

$C up -d --wait postgres
$C run --rm -e WRENCH_BACKUP_POSTGRES_DSN=postgres://wrench:drill-owner@postgres:5432/wrench?sslmode=disable backup restore --from s3 --config-out /restore/config latest ||
	fail "restore failed"
after=$(fingerprint)
echo "after:  $after"
[ "$before" = "$after" ] || fail "restored data differs"

$C up -d --wait wrench
curl -fsS -H "$AUTH" "$W/api/sessions" | grep -q '"id":"drill-3"' || fail "Wrench does not serve restored sessions"
echo "backup drill passed"
