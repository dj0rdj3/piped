package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Compare materialized metadata with reconstructing the same searchable host
// text from canonical membership. Both use the real database and authorization.
func BenchmarkJobHostSearch(b *testing.B) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(b.TempDir(), "data", "piped.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	user, err := db.InitAdmin(ctx, "bench", "hash")
	if err != nil {
		b.Fatal(err)
	}
	if err := db.CreateSession(ctx, user, "bench", time.Now().Unix(), time.Now().Add(time.Hour).Unix()); err != nil {
		b.Fatal(err)
	}
	actor := Actor{User: user, SessionHash: "bench"}
	tx, err := db.beginAuthorized(ctx, actor, ScopeJobsWrite)
	if err != nil {
		b.Fatal(err)
	}
	defer tx.Rollback()
	for i := range 500 {
		id := fmt.Sprintf("host-%04d", i)
		if _, err := tx.ExecContext(ctx, `INSERT INTO hosts(id,name,address,port,username,encrypted_password,run_as_sudo,tags,version,created_at) VALUES(?,?, 'localhost',22,'fixture',?,0,'[]',1,0)`, id, fmt.Sprintf("Target host %04d", i), []byte("encrypted")); err != nil {
			b.Fatal(err)
		}
		if err := saveHostTags(ctx, tx, id, []string{"bench"}); err != nil {
			b.Fatal(err)
		}
		if err := refreshListEntry(ctx, tx, "hosts", id); err != nil {
			b.Fatal(err)
		}
	}
	for i := range 100 {
		id := fmt.Sprintf("job-%04d", i)
		if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,name,script,execution_mode,stop_on_failure,version,created_at,run_as_sudo,schedule_cron,schedule_enabled,schedule_after,last_schedule_minute) VALUES(?,?,'true','parallel',0,1,0,0,'',0,0,0)`, id, id); err != nil {
			b.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO job_host_tags(job_id,tag) VALUES(?,'bench')`, id); err != nil {
			b.Fatal(err)
		}
		if err := refreshListEntry(ctx, tx, "jobs", id); err != nil {
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	query := "target host 0499"
	b.Run("IndexedPage", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			page, err := db.JobsPage(ctx, actor, ListRequest{Limit: 50, Query: query})
			if err != nil || len(page.Items) != 50 || page.Info.Matched != 100 {
				b.Fatal("indexed search", err, page.Info)
			}
		}
	})
	b.Run("CanonicalMembershipText", func(b *testing.B) {
		b.ReportAllocs()
		membership := strings.ReplaceAll(effectiveHostMembership, "?", "definition.id")
		for b.Loop() {
			tx, err := db.beginAuthorized(ctx, actor, ScopeJobsRead)
			if err != nil {
				b.Fatal(err)
			}
			rows, err := tx.QueryContext(ctx, `SELECT definition.id,h.name FROM jobs definition JOIN hosts h ON `+membership+` ORDER BY definition.id,lower(h.name),h.name,h.id`)
			if err != nil {
				tx.Rollback()
				b.Fatal(err)
			}
			text := map[string]*strings.Builder{}
			for rows.Next() {
				var id, name string
				if err := rows.Scan(&id, &name); err != nil {
					b.Fatal(err)
				}
				if text[id] == nil {
					text[id] = &strings.Builder{}
				}
				text[id].WriteString(name)
				text[id].WriteByte(' ')
			}
			err = rows.Err()
			rows.Close()
			tx.Rollback()
			if err != nil {
				b.Fatal(err)
			}
			matches := 0
			for _, value := range text {
				if strings.Contains(strings.ToLower(value.String()), query) {
					matches++
				}
			}
			if matches != 100 {
				b.Fatal("canonical membership search", matches)
			}
		}
	})
}
