// Command convoseed materialises internal/selfconvo/corpus's generated
// templates and links into the app store, as convo.Template rows with
// Origin=convo.OriginCorpus and their outgoing convo.TemplateLink edges.
//
// # SAFETY
//
// Every DSN that is not a "sqlite:" one is, in this repo, the SHARED
// production MySQL — there is no separate test server (see CLAUDE.md's
// "production MySQL is shared and is not a test target"). convoseed
// refuses to write to one unless -i-know-this-is-production is passed, and
// even then it prints only the host/port/database it resolved from the
// DSN, never the credential (see describeTarget).
//
// # WHY -replace EXISTS
//
// wa_convo_templates has no uniqueness constraint on name, so re-running
// convoseed without -replace inserts a second copy of every template
// instead of erroring. -replace deletes every existing origin='corpus' row
// first (cascading its links, see migrations/0008_convo_scheduling.sql),
// leaving any operator upload (origin='upload') untouched — that is what
// makes a re-run idempotent in template/link COUNT rather than doubling
// it every time.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/convo"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/id"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/selfconvo/corpus"
)

// DefaultSeed is used when -seed is not passed, so an operator's plain
// `convoseed -replace` is reproducible run to run without having to
// remember or record a seed of their own.
const DefaultSeed uint64 = 20260910

// ErrProductionConfirmationRequired is returned when dsn resolves to
// anything but a local SQLite file and -i-know-this-is-production was not
// passed.
var ErrProductionConfirmationRequired = errors.New(
	"refusing to write to a non-sqlite DSN without -i-know-this-is-production — " +
		"the only MySQL a dev machine can reach is the SHARED production server (see CLAUDE.md)")

type flags struct {
	dsn       string
	seed      uint64
	replace   bool
	dryRun    bool
	limit     int
	iKnowProd bool
}

func parseFlags(args []string) (flags, error) {
	fs := flag.NewFlagSet("convoseed", flag.ContinueOnError)
	f := flags{}
	fs.StringVar(&f.dsn, "dsn", os.Getenv("WSM_APP_DSN"), "app store DSN (defaults to WSM_APP_DSN)")
	fs.Uint64Var(&f.seed, "seed", DefaultSeed, "deterministic seed passed to corpus.Generate")
	fs.BoolVar(&f.replace, "replace", false, "delete existing origin=\"corpus\" templates+links first, leaving uploads alone")
	fs.BoolVar(&f.dryRun, "dry-run", false, "generate and report counts, write nothing")
	fs.IntVar(&f.limit, "limit", 0, "cap the number of templates inserted (0 = no cap)")
	fs.BoolVar(&f.iKnowProd, "i-know-this-is-production", false,
		"required to run against a non-sqlite DSN")
	if err := fs.Parse(args); err != nil {
		return flags{}, err
	}
	return f, nil
}

// describeTarget reports what convoseed is about to write to, WITHOUT ever
// including a credential: for a sqlite: DSN that is the path itself (no
// secret to leak); for anything else it parses only Net/Addr/DBName via
// the mysql driver's own DSN parser and discards User/Passwd entirely.
func describeTarget(dsn string) (desc string, isSQLite bool, err error) {
	if strings.HasPrefix(dsn, db.SQLiteAppPrefix) {
		return dsn, true, nil
	}
	cfg, perr := gomysql.ParseDSN(dsn)
	if perr != nil {
		return "", false, fmt.Errorf("parse dsn: %w", perr)
	}
	return fmt.Sprintf("mysql net=%s addr=%s db=%s", cfg.Net, cfg.Addr, cfg.DBName), false, nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		log.Fatalf("convoseed: %v", err)
	}
}

func run(args []string, stdout io.Writer) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	if f.dsn == "" {
		return errors.New("dsn is required: pass -dsn or set WSM_APP_DSN")
	}

	target, isSQLite, err := describeTarget(f.dsn)
	if err != nil {
		return fmt.Errorf("describe dsn: %w", err)
	}
	fmt.Fprintf(stdout, "target: %s\n", target)
	if !isSQLite && !f.iKnowProd {
		return fmt.Errorf("%w (resolved target: %s)", ErrProductionConfirmationRequired, target)
	}

	start := time.Now()
	packs, err := corpus.Packs()
	if err != nil {
		return fmt.Errorf("load packs: %w", err)
	}
	generated, err := corpus.Generate(packs, f.seed)
	if err != nil {
		return fmt.Errorf("generate corpus: %w", err)
	}
	totalGenerated := len(generated)

	if f.limit > 0 && f.limit < len(generated) {
		generated = generated[:f.limit]
	}
	// A -limit cut can leave a kept template's link pointing at a
	// now-excluded one; drop those edges rather than inserting a link to a
	// template id that will never exist.
	allowed := make(map[string]bool, len(generated))
	for _, g := range generated {
		allowed[g.Name] = true
	}
	totalLinks := 0
	for i := range generated {
		kept := generated[i].Links[:0:0]
		for _, l := range generated[i].Links {
			if allowed[l.ToName] {
				kept = append(kept, l)
			}
		}
		generated[i].Links = kept
		totalLinks += len(kept)
	}

	fmt.Fprintf(stdout, "packs loaded: %d\n", len(packs))
	fmt.Fprintf(stdout, "templates generated: %d\n", totalGenerated)
	fmt.Fprintf(stdout, "templates to insert: %d\n", len(generated))
	fmt.Fprintf(stdout, "links to insert: %d\n", totalLinks)

	if f.dryRun {
		fmt.Fprintf(stdout, "dry-run: wrote nothing (elapsed %s)\n", time.Since(start))
		return nil
	}

	ctx := context.Background()
	pool, err := db.OpenAppStore(ctx, f.dsn)
	if err != nil {
		return fmt.Errorf("open app store: %w", err)
	}
	defer pool.Close()

	if f.replace {
		// wa_convo_template_links cascades off BOTH endpoints (see
		// migrations/0008_convo_scheduling.sql), so deleting the templates
		// alone also removes every link touching one — nothing origin=
		// 'upload' is ever a target of this DELETE.
		if _, err := pool.ExecContext(ctx,
			`DELETE FROM wa_convo_templates WHERE origin = ?`, convo.OriginCorpus); err != nil {
			return fmt.Errorf("delete existing corpus templates: %w", err)
		}
	}

	repo := convo.NewRepository(pool)
	nameToID := make(map[string]string, len(generated))
	for _, g := range generated {
		tid := id.New()
		nameToID[g.Name] = tid
		tmpl := &convo.Template{
			ID:        tid,
			Name:      g.Name,
			Topic:     g.Topic,
			Stage:     g.Stage,
			Origin:    convo.OriginCorpus,
			TurnCount: len(g.Script.Turns),
			Body:      g.Body,
		}
		if err := repo.InsertTemplate(ctx, tmpl); err != nil {
			return fmt.Errorf("insert template %q: %w", g.Name, err)
		}
	}

	insertedLinks := 0
	for _, g := range generated {
		links := make([]convo.TemplateLink, 0, len(g.Links))
		for _, l := range g.Links {
			toID, ok := nameToID[l.ToName]
			if !ok {
				// Already filtered above; defensive, never expected to fire.
				continue
			}
			links = append(links, convo.TemplateLink{
				ToTemplateID:  toID,
				Weight:        l.Weight,
				MinGapSeconds: l.MinGapSeconds,
				MaxGapSeconds: l.MaxGapSeconds,
			})
		}
		if err := repo.ReplaceTemplateLinks(ctx, nameToID[g.Name], links); err != nil {
			return fmt.Errorf("insert links for %q: %w", g.Name, err)
		}
		insertedLinks += len(links)
	}

	fmt.Fprintf(stdout, "inserted %d templates, %d links (elapsed %s)\n",
		len(generated), insertedLinks, time.Since(start))
	return nil
}
