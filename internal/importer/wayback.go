package importer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"

	"git.mp.ls/mpls/shrike/internal/repository"
)

// WaybackImporter retrieves historical WHOIS data from the Internet Archive
// Wayback Machine. It uses the CDX API to find archived WHOIS lookup pages
// and the id_ URL pattern to fetch clean (unrewritten) HTML for parsing.
type WaybackImporter struct {
	domainRepo *repository.DomainRepository
	pool       *pgxpool.Pool
	client     *http.Client
	limiter    *rate.Limiter
	workers    int
}

type WaybackStats struct {
	DomainsProcessed atomic.Int64
	DomainsSkipped   atomic.Int64
	CapturesFound    atomic.Int64
	SnapshotsStored  atomic.Int64
	Errors           atomic.Int64
}

// WHOIS lookup sites with their URL patterns
var waybackWhoisSites = map[string]string{
	"whois":       "who.is/whois/",
	"whoiscom":    "www.whois.com/whois/",
	"domaintools": "whois.domaintools.com/",
}

func NewWaybackImporter(domainRepo *repository.DomainRepository, pool *pgxpool.Pool, workers int) *WaybackImporter {
	if workers <= 0 {
		workers = 3
	}
	// Internet Archive CDX API limit: 60 req/min. Stay under at 45 req/min
	// shared across all workers. Burst of 3 to smooth out small gaps.
	limiter := rate.NewLimiter(rate.Limit(0.75), 3)

	return &WaybackImporter{
		domainRepo: domainRepo,
		pool:       pool,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		limiter: limiter,
		workers: workers,
	}
}

// ImportKnownDomains searches the Wayback Machine for historical WHOIS data
// for domains already in the database. Resumes automatically by skipping
// domains that already have wayback_archive snapshots.
func (w *WaybackImporter) ImportKnownDomains(ctx context.Context, limit int) (*WaybackStats, error) {
	stats := &WaybackStats{}

	// Fetch domains that do NOT already have wayback_archive snapshots (resume support)
	query := `SELECT d.id, d.name FROM domains d
		WHERE NOT EXISTS (
			SELECT 1 FROM domain_snapshots ds
			WHERE ds.domain_id = d.id AND ds.source = 'wayback_archive'
		)
		ORDER BY d.id`
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := w.pool.Query(ctx, query)
	if err != nil {
		return stats, fmt.Errorf("querying domains: %w", err)
	}
	defer rows.Close()

	type domainRow struct {
		ID   int64
		Name string
	}
	var domains []domainRow
	for rows.Next() {
		var d domainRow
		if err := rows.Scan(&d.ID, &d.Name); err != nil {
			return stats, err
		}
		domains = append(domains, d)
	}
	rows.Close()

	log.Printf("Wayback import: %d domains to process (%d workers, 15 req/sec)",
		len(domains), w.workers)

	// Fan out to worker pool
	work := make(chan domainRow, w.workers*2)
	var wg sync.WaitGroup

	for i := 0; i < w.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for d := range work {
				if ctx.Err() != nil {
					return
				}
				domainStats, err := w.importDomainCaptures(ctx, d.Name, d.ID)
				if err != nil {
					log.Printf("  Error processing %s: %v", d.Name, err)
					stats.Errors.Add(1)
					continue
				}
				stats.DomainsProcessed.Add(1)
				stats.CapturesFound.Add(int64(domainStats.captures))
				stats.SnapshotsStored.Add(int64(domainStats.stored))

				processed := stats.DomainsProcessed.Load()
				if processed%100 == 0 {
					log.Printf("Wayback progress: %d/%d domains, %d captures, %d snapshots stored, %d errors",
						processed, len(domains),
						stats.CapturesFound.Load(),
						stats.SnapshotsStored.Load(),
						stats.Errors.Load())
				}
			}
		}()
	}

	for _, d := range domains {
		if ctx.Err() != nil {
			break
		}
		work <- d
	}
	close(work)
	wg.Wait()

	return stats, nil
}

// ImportDomain searches for a specific domain's WHOIS history.
func (w *WaybackImporter) ImportDomain(ctx context.Context, name string) (*WaybackStats, error) {
	stats := &WaybackStats{}

	tld := name
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		tld = name[idx+1:]
	}
	domainID, err := w.domainRepo.Upsert(ctx, name, tld)
	if err != nil {
		return nil, fmt.Errorf("upserting domain: %w", err)
	}

	ds, err := w.importDomainCaptures(ctx, name, domainID)
	if err != nil {
		return nil, err
	}
	stats.DomainsProcessed.Add(1)
	stats.CapturesFound.Add(int64(ds.captures))
	stats.SnapshotsStored.Add(int64(ds.stored))

	return stats, nil
}

// ScanSite finds all domains archived on a specific WHOIS lookup site.
func (w *WaybackImporter) ScanSite(ctx context.Context, site string, limit int) (*WaybackStats, error) {
	stats := &WaybackStats{}

	prefix, ok := waybackWhoisSites[site]
	if !ok {
		return nil, fmt.Errorf("unknown site %q (valid: whois, whoiscom, domaintools)", site)
	}

	cdxURL := fmt.Sprintf(
		"http://web.archive.org/cdx/search/cdx?url=%s*&output=json&fl=original,timestamp&filter=statuscode:200&collapse=urlkey",
		prefix)
	if limit > 0 {
		cdxURL += fmt.Sprintf("&limit=%d", limit)
	}

	log.Printf("Scanning Wayback Machine for %s...", prefix)

	captures, err := w.queryCDX(ctx, cdxURL)
	if err != nil {
		return stats, fmt.Errorf("querying CDX: %w", err)
	}

	log.Printf("Found %d unique URLs", len(captures))

	// Group captures by domain
	domainCaptures := make(map[string][]waybackCapture)
	for _, cap := range captures {
		domain := extractDomainFromURL(cap.URL)
		if domain != "" {
			domainCaptures[domain] = append(domainCaptures[domain], cap)
		}
	}

	log.Printf("Covering %d unique domains", len(domainCaptures))

	for domain, caps := range domainCaptures {
		stats.DomainsProcessed.Add(1)
		stats.CapturesFound.Add(int64(len(caps)))

		tld := domain
		if idx := strings.LastIndex(domain, "."); idx >= 0 {
			tld = domain[idx+1:]
		}
		domainID, err := w.domainRepo.Upsert(ctx, domain, tld)
		if err != nil {
			log.Printf("  Error upserting %s: %v", domain, err)
			stats.Errors.Add(1)
			continue
		}

		for _, cap := range caps {
			stored, err := w.processCapture(ctx, cap, domain, domainID)
			if err != nil {
				stats.Errors.Add(1)
				continue
			}
			if stored {
				stats.SnapshotsStored.Add(1)
			}
		}

		processed := stats.DomainsProcessed.Load()
		if processed%100 == 0 {
			log.Printf("Scan progress: %d domains, %d snapshots stored",
				processed, stats.SnapshotsStored.Load())
		}
	}

	return stats, nil
}

type domainResult struct {
	captures int
	stored   int
}

type waybackCapture struct {
	URL       string
	Timestamp string
}

func (w *WaybackImporter) importDomainCaptures(ctx context.Context, name string, domainID int64) (*domainResult, error) {
	result := &domainResult{}

	for siteName, prefix := range waybackWhoisSites {
		targetURL := prefix + name
		cdxURL := fmt.Sprintf(
			"http://web.archive.org/cdx/search/cdx?url=%s&output=json&fl=original,timestamp&filter=statuscode:200",
			targetURL)

		captures, err := w.queryCDX(ctx, cdxURL)
		if err != nil {
			continue
		}

		if len(captures) > 0 {
			log.Printf("  %s: %d captures on %s", name, len(captures), siteName)
		}

		result.captures += len(captures)

		for _, cap := range captures {
			stored, err := w.processCapture(ctx, cap, name, domainID)
			if err != nil {
				continue
			}
			if stored {
				result.stored++
			}
		}
	}

	return result, nil
}

func (w *WaybackImporter) processCapture(ctx context.Context, cap waybackCapture, domain string, domainID int64) (bool, error) {
	observedAt, err := time.Parse("20060102150405", cap.Timestamp)
	if err != nil {
		return false, fmt.Errorf("parsing timestamp: %w", err)
	}

	pageURL := fmt.Sprintf("https://web.archive.org/web/%sid_/%s", cap.Timestamp, cap.URL)

	// Rate limit instead of fixed sleep
	if err := w.limiter.Wait(ctx); err != nil {
		return false, err
	}

	html, err := w.fetchPage(ctx, pageURL)
	if err != nil {
		return false, err
	}

	whoisText := extractWhoisFromHTML(html)
	if whoisText == "" {
		return false, nil
	}

	snap := parseRawWhoisText(whoisText, observedAt)
	if snap == nil {
		return false, nil
	}

	snap.DomainID = domainID
	snap.Source = "wayback_archive"
	snap.RawWhois = &whoisText

	if snap.RegistrantEmail != nil || snap.RegistrantName != nil {
		snap.ContainsPII = true
	}

	stored, err := w.domainRepo.InsertSnapshotIfChanged(ctx, snap)
	if err != nil {
		return false, fmt.Errorf("inserting snapshot: %w", err)
	}

	return stored, nil
}

func (w *WaybackImporter) queryCDX(ctx context.Context, cdxURL string) ([]waybackCapture, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", cdxURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Shrike/1.0 (WHOIS history database; https://shrike.mp.ls; contact: patrick@mp.ls)")

	if err := w.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	var resp *http.Response
	for attempt := 0; attempt < 3; attempt++ {
		resp, err = w.client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == 429 {
			resp.Body.Close()
			wait := time.Duration(60*(attempt+1)) * time.Second
			log.Printf("  429 rate limited on CDX, backing off %v", wait)
			select {
			case <-time.After(wait):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		break
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, nil
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 50*1024*1024))
	if err != nil {
		return nil, err
	}

	// Try JSON array format first
	var rows [][]string
	if err := json.Unmarshal(body, &rows); err == nil && len(rows) > 1 {
		var captures []waybackCapture
		for _, row := range rows[1:] { // Skip header row
			if len(row) >= 2 {
				captures = append(captures, waybackCapture{
					URL:       row[0],
					Timestamp: row[1],
				})
			}
		}
		return captures, nil
	}

	// Fallback: newline-delimited text
	var captures []waybackCapture
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 {
			captures = append(captures, waybackCapture{
				URL:       fields[0],
				Timestamp: fields[1],
			})
		}
	}

	return captures, nil
}

func (w *WaybackImporter) fetchPage(ctx context.Context, pageURL string) (string, error) {
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "GET", pageURL, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", "Shrike/1.0 (WHOIS history database; https://shrike.mp.ls; contact: patrick@mp.ls)")

		resp, err := w.client.Do(req)
		if err != nil {
			return "", err
		}

		if resp.StatusCode == 429 {
			resp.Body.Close()
			wait := time.Duration(60*(attempt+1)) * time.Second
			log.Printf("  429 rate limited on fetch, backing off %v", wait)
			select {
			case <-time.After(wait):
				continue
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
		resp.Body.Close()
		if err != nil {
			return "", err
		}

		if resp.StatusCode != 200 {
			return "", fmt.Errorf("HTTP %d", resp.StatusCode)
		}

		return string(body), nil
	}
	return "", fmt.Errorf("giving up after 3 attempts (429)")
}
