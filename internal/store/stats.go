package store

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ordo-systems/wrench/internal/money"
)

// Observed is a sum over the attempts that reported a field. Sum is nil when Reported is zero, so a
// chart shows missing telemetry instead of zero.
type Observed struct {
	Sum      *int64 `json:"sum"`
	Reported int64  `json:"reported"`
}

// Latency holds percentiles in milliseconds over attempts with the observation; nil without any.
type Latency struct {
	Observed int64    `json:"observed"`
	P50      *float64 `json:"p50_ms"`
	P95      *float64 `json:"p95_ms"`
}

// StatsBucket aggregates the attempts that started in [Start, Start+bucket), or all of them for totals.
// Cost counts confirmed attempts only; Pending counts finished attempts whose cost is unknown.
// Attempts minus Requests is the number of additional attempts made for the same logical request.
type StatsBucket struct {
	Start      time.Time    `json:"start"`
	Attempts   int64        `json:"attempts"`
	Requests   int64        `json:"requests"`
	Success    int64        `json:"success"`
	Errors     int64        `json:"errors"`
	Cancelled  int64        `json:"cancelled"`
	Running    int64        `json:"running"`
	Pending    int64        `json:"pending"`
	Confirmed  int64        `json:"confirmed"`
	Cost       money.Amount `json:"cost"`
	Input      Observed     `json:"input_tokens"`
	Output     Observed     `json:"output_tokens"`
	Reasoning  Observed     `json:"reasoning_tokens"`
	CacheRead  Observed     `json:"cache_read_tokens"`
	CacheWrite Observed     `json:"cache_write_tokens"`
	// CachedInput and CacheableInput are the cache share's numerator and denominator over attempts that
	// reported both input and cache-read tokens; both are zero when none did.
	CachedInput    int64   `json:"cached_input"`
	CacheableInput int64   `json:"cacheable_input"`
	FirstToken     Latency `json:"first_token"`
	Duration       Latency `json:"duration"`
	// ActiveSessions counts sessions with a request in the bucket; nil for totals.
	ActiveSessions *int64 `json:"active_sessions,omitempty"`
}

// Breakdown is spend of one user, model or inference provider.
type Breakdown struct {
	Key      string       `json:"key"`
	Attempts int64        `json:"attempts"`
	Pending  int64        `json:"pending"`
	Cost     money.Amount `json:"cost"`
}

// SessionCosts describes confirmed spend per session. Incomplete counts sessions that still have attempts with unknown cost.
type SessionCosts struct {
	Sessions   int64         `json:"sessions"`
	Incomplete int64         `json:"incomplete"`
	Average    *money.Amount `json:"average"`
	P50        *money.Amount `json:"p50"`
	P95        *money.Amount `json:"p95"`
}

// Stats is the dashboard's aggregate view of the journal for one filter.
type Stats struct {
	Buckets       []StatsBucket `json:"buckets"`
	Total         StatsBucket   `json:"total"`
	ByUser        []Breakdown   `json:"by_user"`
	ByModel       []Breakdown   `json:"by_model"`
	ByProvider    []Breakdown   `json:"by_provider"`
	Sessions      SessionCosts  `json:"sessions"`
	OldestPending *time.Time    `json:"oldest_pending"`
}

const bucketColumns = `COUNT(*), COUNT(DISTINCT request_id),
	COUNT(*) FILTER (WHERE outcome = 'success'), COUNT(*) FILTER (WHERE outcome = 'error'),
	COUNT(*) FILTER (WHERE outcome = 'cancelled'), COUNT(*) FILTER (WHERE outcome IS NULL),
	COUNT(*) FILTER (WHERE cost_state = 'pending' AND finished_at IS NOT NULL),
	COUNT(*) FILTER (WHERE cost_state = 'confirmed'),
	COALESCE(SUM(cost), 0)::text,
	SUM(input_tokens)::bigint, COUNT(input_tokens), SUM(output_tokens)::bigint, COUNT(output_tokens),
	SUM(reasoning_tokens)::bigint, COUNT(reasoning_tokens), SUM(cache_read_tokens)::bigint, COUNT(cache_read_tokens),
	SUM(cache_write_tokens)::bigint, COUNT(cache_write_tokens),
	COALESCE(SUM(cache_read_tokens) FILTER (WHERE input_tokens IS NOT NULL), 0)::bigint,
	COALESCE(SUM(input_tokens) FILTER (WHERE cache_read_tokens IS NOT NULL), 0)::bigint,
	COUNT(first_token_at),
	percentile_cont(0.5) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM first_token_at - started_at) * 1000) FILTER (WHERE first_token_at IS NOT NULL),
	percentile_cont(0.95) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM first_token_at - started_at) * 1000) FILTER (WHERE first_token_at IS NOT NULL),
	COUNT(finished_at),
	percentile_cont(0.5) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM finished_at - started_at) * 1000) FILTER (WHERE finished_at IS NOT NULL),
	percentile_cont(0.95) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM finished_at - started_at) * 1000) FILTER (WHERE finished_at IS NOT NULL)`

// bucketTargets lists the scan destinations of bucketColumns; cost receives the textual sum.
func bucketTargets(b *StatsBucket, cost *string) []any {
	return []any{&b.Attempts, &b.Requests, &b.Success, &b.Errors, &b.Cancelled, &b.Running, &b.Pending,
		&b.Confirmed, cost,
		&b.Input.Sum, &b.Input.Reported, &b.Output.Sum, &b.Output.Reported, &b.Reasoning.Sum, &b.Reasoning.Reported,
		&b.CacheRead.Sum, &b.CacheRead.Reported, &b.CacheWrite.Sum, &b.CacheWrite.Reported,
		&b.CachedInput, &b.CacheableInput,
		&b.FirstToken.Observed, &b.FirstToken.P50, &b.FirstToken.P95,
		&b.Duration.Observed, &b.Duration.P50, &b.Duration.P95}
}

func scanBucket(r pgx.Row) (*time.Time, StatsBucket, error) {
	var (
		start *time.Time
		b     StatsBucket
		cost  string
	)
	err := r.Scan(append([]any{&start}, bucketTargets(&b, &cost)...)...)
	if err != nil {
		return nil, b, err
	}
	b.Cost, err = money.Parse(cost)
	return start, b, err
}

// Stats aggregates attempts matching f, which must have From and To, into buckets of the given width
// aligned to From. Buckets without attempts are present with zero counts and nil sums, so charts keep
// a continuous time axis. Totals and buckets come from one grouping-sets query, so they always agree.
func (s *Store) Stats(ctx context.Context, f AttemptFilter, bucket time.Duration) (Stats, error) {
	if f.From.IsZero() || !f.To.After(f.From) || bucket <= 0 {
		return Stats{}, fmt.Errorf("store: stats need a period and a bucket width")
	}
	where, args := f.where()
	args = append(args, bucket, f.From)
	bin := fmt.Sprintf("date_bin($%d::interval, started_at, $%d::timestamptz)", len(args)-1, len(args))
	rows, err := s.pool.Query(ctx, "SELECT "+bin+" AS b, "+bucketColumns+" FROM attempts "+where+
		" GROUP BY GROUPING SETS (("+bin+"), ()) ORDER BY b NULLS FIRST", args...)
	if err != nil {
		return Stats{}, err
	}
	defer rows.Close()
	st := Stats{}
	byStart := map[time.Time]StatsBucket{}
	for rows.Next() {
		start, b, err := scanBucket(rows)
		if err != nil {
			return Stats{}, err
		}
		if start == nil {
			st.Total = b
			continue
		}
		byStart[start.UTC()] = b
	}
	if err := rows.Err(); err != nil {
		return Stats{}, err
	}
	active, err := s.activeSessions(ctx, f, bucket)
	if err != nil {
		return Stats{}, err
	}
	st.Total.Start = f.From
	for t := f.From.UTC(); t.Before(f.To); t = t.Add(bucket) {
		b := byStart[t]
		b.Start = t
		n := active[t]
		b.ActiveSessions = &n
		st.Buckets = append(st.Buckets, b)
	}
	if st.ByUser, err = s.breakdown(ctx, "user_email", f); err != nil {
		return Stats{}, err
	}
	if st.ByModel, err = s.breakdown(ctx, "requested_model", f); err != nil {
		return Stats{}, err
	}
	if st.ByProvider, err = s.breakdown(ctx, "inference_provider", f); err != nil {
		return Stats{}, err
	}
	if st.Sessions, err = s.sessionCosts(ctx, f); err != nil {
		return Stats{}, err
	}
	st.OldestPending, err = s.oldestPending(ctx, f)
	return st, err
}

// activeSessions counts sessions present per bucket. Only the user and session filters apply:
// presence has no model, provider or agent.
func (s *Store) activeSessions(ctx context.Context, f AttemptFilter, bucket time.Duration) (map[time.Time]int64, error) {
	byUser, err := s.activeSessionsByUser(ctx, f, bucket)
	if err != nil {
		return nil, err
	}
	out := map[time.Time]int64{}
	for _, counts := range byUser {
		addPresence(out, counts)
	}
	return out, nil
}

// activeSessionsByUser counts sessions present per user and bucket. A session has one owner, so the counts
// of several users add up to the sessions present among them.
func (s *Store) activeSessionsByUser(ctx context.Context, f AttemptFilter, bucket time.Duration) (map[string]map[time.Time]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.user_email, date_bin($1::interval, p.bucket, $2::timestamptz) AS b, COUNT(DISTINCT p.session_id)
		FROM session_presence p JOIN sessions s ON s.id = p.session_id
		WHERE p.bucket >= $2 AND p.bucket < $3 AND ($4 = '' OR s.user_email = $4)
			AND (cardinality($5::text[]) = 0 OR p.session_id = ANY($5))
		GROUP BY s.user_email, b`, bucket, f.From, f.To, f.UserEmail, nonNilStrings(f.SessionIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[time.Time]int64{}
	for rows.Next() {
		var (
			user string
			t    time.Time
			n    int64
		)
		if err := rows.Scan(&user, &t, &n); err != nil {
			return nil, err
		}
		if out[user] == nil {
			out[user] = map[time.Time]int64{}
		}
		out[user][t.UTC()] = n
	}
	return out, rows.Err()
}

// addPresence adds per-bucket session counts into sum.
func addPresence(sum, counts map[time.Time]int64) {
	for t, n := range counts {
		sum[t] += n
	}
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// breakdown groups spend by a column taken only from the constants in Stats, never from input.
func (s *Store) breakdown(ctx context.Context, column string, f AttemptFilter) ([]Breakdown, error) {
	where, args := f.where()
	rows, err := s.pool.Query(ctx, "SELECT "+column+", COUNT(*), "+
		"COUNT(*) FILTER (WHERE cost_state = 'pending' AND finished_at IS NOT NULL), COALESCE(SUM(cost), 0)::text "+
		"FROM attempts "+where+" GROUP BY "+column+" ORDER BY SUM(cost) DESC NULLS LAST, "+column, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Breakdown, error) {
		var (
			b    Breakdown
			cost string
		)
		if err := r.Scan(&b.Key, &b.Attempts, &b.Pending, &cost); err != nil {
			return b, err
		}
		var err error
		b.Cost, err = money.Parse(cost)
		return b, err
	})
}

// sessionCosts uses percentile_disc so the percentiles are exact recorded amounts, not float estimates.
func (s *Store) sessionCosts(ctx context.Context, f AttemptFilter) (SessionCosts, error) {
	where, args := f.where()
	var (
		out           SessionCosts
		avg, p50, p95 *string
	)
	err := s.pool.QueryRow(ctx, `
		WITH per AS (
			SELECT session_id, COALESCE(SUM(cost), 0) AS cost,
				bool_or(cost_state = 'pending' AND finished_at IS NOT NULL) AS incomplete
			FROM attempts `+where+` GROUP BY session_id)
		SELECT COUNT(*), COUNT(*) FILTER (WHERE incomplete),
			round(AVG(cost), 9)::text,
			(percentile_disc(0.5) WITHIN GROUP (ORDER BY cost))::text,
			(percentile_disc(0.95) WITHIN GROUP (ORDER BY cost))::text
		FROM per`, args...).Scan(&out.Sessions, &out.Incomplete, &avg, &p50, &p95)
	if err != nil {
		return out, err
	}
	for _, v := range []struct {
		src *string
		dst **money.Amount
	}{{avg, &out.Average}, {p50, &out.P50}, {p95, &out.P95}} {
		if v.src == nil {
			continue
		}
		a, err := money.Parse(*v.src)
		if err != nil {
			return out, err
		}
		*v.dst = &a
	}
	return out, nil
}

// oldestPending is the finish time of the oldest attempt whose cost is still unknown.
func (s *Store) oldestPending(ctx context.Context, f AttemptFilter) (*time.Time, error) {
	where, args := f.where()
	cond := "WHERE"
	if where != "" {
		cond = where + " AND"
	}
	var t *time.Time
	err := s.pool.QueryRow(ctx, "SELECT MIN(finished_at) FROM attempts "+cond+
		" cost_state = 'pending' AND finished_at IS NOT NULL", args...).Scan(&t)
	return utcPtr(t), err
}

// UserSeries is the bucket series of one user, or of all users outside the top ones when User is empty.
type UserSeries struct {
	User    string        `json:"user"`
	Buckets []StatsBucket `json:"buckets"`
}

// StatsByUser returns a bucket series for each of the top users by confirmed cost, highest first with ties
// by email, aligned like Stats. When more than top users had attempts, a final series with an empty User sums
// the rest; it has no percentiles and no cache or token-sum observations, because those do not add up.
func (s *Store) StatsByUser(ctx context.Context, f AttemptFilter, bucket time.Duration, top int) ([]UserSeries, error) {
	if f.From.IsZero() || !f.To.After(f.From) || bucket <= 0 {
		return nil, fmt.Errorf("store: stats need a period and a bucket width")
	}
	where, args := f.where()
	args = append(args, bucket, f.From)
	bin := fmt.Sprintf("date_bin($%d::interval, started_at, $%d::timestamptz)", len(args)-1, len(args))
	rows, err := s.pool.Query(ctx, "SELECT user_email, "+bin+" AS b, "+bucketColumns+" FROM attempts "+where+
		" GROUP BY user_email, "+bin, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byUser := map[string]map[time.Time]StatsBucket{}
	spent := map[string]money.Amount{}
	for rows.Next() {
		var (
			user  string
			start time.Time
			b     StatsBucket
			cost  string
		)
		if err := rows.Scan(append([]any{&user, &start}, bucketTargets(&b, &cost)...)...); err != nil {
			return nil, err
		}
		if b.Cost, err = money.Parse(cost); err != nil {
			return nil, err
		}
		if byUser[user] == nil {
			byUser[user] = map[time.Time]StatsBucket{}
		}
		byUser[user][start.UTC()] = b
		spent[user] += b.Cost
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	users := make([]string, 0, len(byUser))
	for u := range byUser {
		users = append(users, u)
	}
	slices.SortFunc(users, func(a, b string) int {
		if c := cmp.Compare(spent[b], spent[a]); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
	presence, err := s.activeSessionsByUser(ctx, f, bucket)
	if err != nil {
		return nil, err
	}
	topUsers := users[:min(top, len(users))]
	series := []UserSeries{}
	for _, u := range topUsers {
		series = append(series, UserSeries{User: u, Buckets: alignBuckets(f, bucket, byUser[u], presence[u])})
	}
	if len(users) > top {
		rest := map[time.Time]StatsBucket{}
		for _, u := range users[top:] {
			for t, b := range byUser[u] {
				rest[t] = addCounts(rest[t], b)
			}
		}
		series = append(series, UserSeries{Buckets: alignBuckets(f, bucket, rest, otherPresence(presence, topUsers))})
	}
	return series, nil
}

// otherPresence sums the session counts of every user outside top, including users present without attempts.
func otherPresence(presence map[string]map[time.Time]int64, top []string) map[time.Time]int64 {
	out := map[time.Time]int64{}
	for u, counts := range presence {
		if !slices.Contains(top, u) {
			addPresence(out, counts)
		}
	}
	return out
}

// alignBuckets lists every bucket of the period, empty ones included, so a series has a continuous time axis.
// active gives each bucket's active sessions; a bucket without presence has zero.
func alignBuckets(f AttemptFilter, bucket time.Duration, have map[time.Time]StatsBucket, active map[time.Time]int64) []StatsBucket {
	var out []StatsBucket
	for t := f.From.UTC(); t.Before(f.To); t = t.Add(bucket) {
		b := have[t]
		b.Start = t
		n := active[t]
		b.ActiveSessions = &n
		out = append(out, b)
	}
	return out
}

// addCounts adds the additive fields of b to sum. Percentiles, cache shares and observation counts stay unset.
func addCounts(sum, b StatsBucket) StatsBucket {
	sum.Attempts += b.Attempts
	sum.Requests += b.Requests // a request has one owner, so per-user counts do not overlap
	sum.Success += b.Success
	sum.Errors += b.Errors
	sum.Cancelled += b.Cancelled
	sum.Running += b.Running
	sum.Pending += b.Pending
	sum.Confirmed += b.Confirmed
	sum.Cost += b.Cost
	sum.Input = addObserved(sum.Input, b.Input)
	sum.Output = addObserved(sum.Output, b.Output)
	sum.Reasoning = addObserved(sum.Reasoning, b.Reasoning)
	sum.CacheWrite = addObserved(sum.CacheWrite, b.CacheWrite)
	return sum
}

func addObserved(a, b Observed) Observed {
	if b.Sum != nil {
		n := *b.Sum
		if a.Sum != nil {
			n += *a.Sum
		}
		a.Sum = &n
	}
	a.Reported += b.Reported
	return a
}
