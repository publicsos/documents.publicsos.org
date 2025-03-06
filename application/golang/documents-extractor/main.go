package main

import (
    "context"
    "crypto/tls"
    "database/sql"
    "encoding/json"
    "fmt"
    "log"
    "net/http"
    "os"
    "os/signal"
    "strings"
    "sync"
    "syscall"
    "time"
    "sync/atomic"

    "github.com/gocolly/colly/v2"
    _ "github.com/mattn/go-sqlite3"
)

type PDFLink struct {
    ID        int    `json:"id"`
    URL       string `json:"url"`
    CreatedAt string `json:"created_at"`
}

type CrawlRequest struct {
    URL string `json:"url"`
}

const (
    colorReset  = "\033[0m"
    colorGreen  = "\033[32m"
    colorYellow = "\033[33m"
    colorCyan   = "\033[36m"
)

var (
    db           *sql.DB
    dbMu         sync.Mutex
    crawlerQueue = make(chan string, 250)
    workerWg     sync.WaitGroup
    numWorkers   = 2
    consoleMu    sync.Mutex
    port         = "8099"
    ctx, cancel  = context.WithCancel(context.Background())
)

func initDB() {
    var err error
    db, err = sql.Open("sqlite3", "./pdf_links.db")
    if err != nil {
        log.Fatalf("Database initialization failed: %v", err)
    }
    
    db.SetMaxOpenConns(25)
    db.SetMaxIdleConns(5)
    db.SetConnMaxLifetime(5 * time.Minute)
    
    createTable := `
    CREATE TABLE IF NOT EXISTS pdf_links (
        id INTEGER PRIMARY KEY,
        url TEXT UNIQUE,
        created_at DATETIME DEFAULT CURRENT_TIMESTAMP
    );`
    _, err = db.Exec(createTable)
    if err != nil {
        log.Fatalf("Table creation failed: %v", err)
    }
    log.Println("Database initialized successfully.")
}

func insertPDFLinks(pdfLinks []string) error {
    if len(pdfLinks) == 0 {
        return nil
    }
    dbMu.Lock()
    defer dbMu.Unlock()
    
    ctx, cancelFunc := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancelFunc()
    
    tx, err := db.BeginTx(ctx, nil)
    if err != nil {
        return fmt.Errorf("begin transaction failed: %w", err)
    }
    defer tx.Rollback()
    
    stmt, err := tx.PrepareContext(ctx, "INSERT OR IGNORE INTO pdf_links(url) VALUES(?)")
    if err != nil {
        return fmt.Errorf("prepare statement failed: %w", err)
    }
    defer stmt.Close()
    
    for _, url := range pdfLinks {
        _, err = stmt.ExecContext(ctx, url)
        if err != nil {
            return fmt.Errorf("execute statement failed: %w", err)
        }
    }
    
    if err := tx.Commit(); err != nil {
        return fmt.Errorf("commit failed: %w", err)
    }
    return nil
}

func getPDFLinks() ([]PDFLink, error) {
    dbMu.Lock()
    defer dbMu.Unlock()
    
    ctx, cancelFunc := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancelFunc()
    
    rows, err := db.QueryContext(ctx, "SELECT id, url, created_at FROM pdf_links ORDER BY created_at DESC")
    if err != nil {
        return nil, fmt.Errorf("query failed: %w", err)
    }
    defer rows.Close()
    
    var links []PDFLink
    for rows.Next() {
        var link PDFLink
        err := rows.Scan(&link.ID, &link.URL, &link.CreatedAt)
        if err != nil {
            return nil, fmt.Errorf("scan failed: %w", err)
        }
        links = append(links, link)
    }
    
    if err := rows.Err(); err != nil {
        return nil, fmt.Errorf("rows iteration failed: %w", err)
    }
    return links, nil
}

func pdfHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }
    
    links, err := getPDFLinks()
    if err != nil {
        log.Printf("Error getting PDF links: %v", err)
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    
    w.Header().Set("Content-Type", "application/json")
    if err := json.NewEncoder(w).Encode(links); err != nil {
        log.Printf("Error encoding JSON: %v", err)
    }
}

func startCrawlerHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }
    
    var req CrawlRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, "Invalid request body", http.StatusBadRequest)
        return
    }
    
    if req.URL == "" {
        http.Error(w, "URL is required", http.StatusBadRequest)
        return
    }
    
    select {
    case crawlerQueue <- req.URL:
        w.Header().Set("Content-Type", "application/json")
        if err := json.NewEncoder(w).Encode(map[string]string{
            "status":  "queued",
            "message": fmt.Sprintf("Crawler started for URL: %s", req.URL),
        }); err != nil {
            log.Printf("Error encoding JSON: %v", err)
        }
    default:
        http.Error(w, "Crawler queue is full, try again later", http.StatusTooManyRequests)
    }
}

func safeColorPrintf(color, format string, args ...interface{}) {
    consoleMu.Lock()
    defer consoleMu.Unlock()
    fmt.Printf(color+format+colorReset+"\n", args...)
}

func createCrawler(workerId int, visitCount *int64, maxVisits int64, pdfLinks *[]string, maxPDFs int) *colly.Collector {
    customTransport := &http.Transport{
        TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
        MaxIdleConns:        100,
        MaxIdleConnsPerHost: 10,
        IdleConnTimeout:     90 * time.Second,
        DisableKeepAlives:   false,
    }
    
    c := colly.NewCollector(
        colly.CacheDir("./colly_cache"),
        colly.MaxDepth(3), // Reduced from 5 to limit crawling scope
        colly.Async(true),
        // Removed AllowURLRevisit to prevent redundant visits
    )
    
    c.WithTransport(customTransport)
    
    c.Limit(&colly.LimitRule{
        DomainGlob:  "*",
        Parallelism: 2,
        Delay:       1 * time.Second,
        RandomDelay: 1 * time.Second,
    })
    
    c.SetRequestTimeout(30 * time.Second)
    c.MaxBodySize = 10 * 1024 * 1024 // Limit response size to 10 MB
    
    c.OnHTML("a[href]", func(e *colly.HTMLElement) {
        select {
        case <-ctx.Done():
            return
        default:
        }
        
        link := e.Attr("href")
        absoluteURL := e.Request.AbsoluteURL(link)
        
        if absoluteURL == "" || strings.HasPrefix(absoluteURL, "javascript:") {
            return
        }
        
        if strings.HasSuffix(absoluteURL, ".pdf") && len(*pdfLinks) < maxPDFs {
            safeColorPrintf(colorGreen, "[Worker %d] PDF LINK: %s", workerId, absoluteURL)
            *pdfLinks = append(*pdfLinks, absoluteURL)
        }
        
        if strings.HasPrefix(absoluteURL, "http") && atomic.LoadInt64(visitCount) < maxVisits {
            select {
            case <-ctx.Done():
                return
            default:
                e.Request.Visit(absoluteURL)
            }
        }
    })
    
    c.OnRequest(func(r *colly.Request) {
        select {
        case <-ctx.Done():
            r.Abort()
            return
        default:
            atomic.AddInt64(visitCount, 1)
            safeColorPrintf(colorCyan, "[Worker %d] VISITING: %s", workerId, r.URL.String())
        }
    })
    
    c.OnError(func(r *colly.Response, err error) {
        safeColorPrintf(colorYellow, "[Worker %d] ERROR: %s - %v", workerId, r.Request.URL, err)
    })
    
    return c
}

func workerRoutine(workerId int) {
    defer workerWg.Done()
    safeColorPrintf(colorCyan, "Worker %d started", workerId)
    
    for {
        select {
        case <-ctx.Done():
            safeColorPrintf(colorYellow, "Worker %d shutting down...", workerId)
            return
        case url, ok := <-crawlerQueue:
            if !ok {
                safeColorPrintf(colorYellow, "Worker %d queue closed, shutting down...", workerId)
                return
            }
            
            safeColorPrintf(colorCyan, "Worker %d processing URL: %s", workerId, url)
            
            var visitCount int64
            maxVisits := int64(1000) // Limit to 1000 pages per crawl
            var pdfLinks []string
            maxPDFs := 1000 // Limit to 1000 PDFs per crawl
            
            c := createCrawler(workerId, &visitCount, maxVisits, &pdfLinks, maxPDFs)
            
            if !strings.HasPrefix(url, "http") {
                url = "https://" + url
                safeColorPrintf(colorYellow, "[Worker %d] Added https:// to URL: %s", workerId, url)
            }
            
            parts := strings.Split(url, "/")
            if len(parts) > 2 {
                domain := parts[2]
                c.AllowedDomains = []string{domain}
                safeColorPrintf(colorCyan, "[Worker %d] Set allowed domain: %s", workerId, domain)
            }
            
            crawlCtx, crawlCancel := context.WithTimeout(ctx, 5*time.Minute)
            
            err := c.Visit(url)
            if err != nil {
                safeColorPrintf(colorYellow, "Worker %d error visiting %s: %v", workerId, url, err)
                crawlCancel()
                continue
            }
            
            done := make(chan bool)
            go func() {
                c.Wait()
                close(done)
            }()
            
            select {
            case <-crawlCtx.Done():
                safeColorPrintf(colorYellow, "Worker %d crawl timeout for URL: %s", workerId, url)
            case <-done:
                safeColorPrintf(colorCyan, "Worker %d finished processing URL: %s", workerId, url)
            }
            
            crawlCancel()
            
            if err := insertPDFLinks(pdfLinks); err != nil {
                safeColorPrintf(colorYellow, "[Worker %d] Error inserting PDF links: %v", workerId, err)
            }
            
            c = nil // Allow garbage collection
            time.Sleep(100 * time.Millisecond)
        }
    }
}

func main() {
    sigs := make(chan os.Signal, 1)
    signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
    
    go func() {
        sig := <-sigs
        safeColorPrintf(colorYellow, "Received signal %v, shutting down gracefully...", sig)
        cancel()
        go func() {
            time.Sleep(5 * time.Second)
            close(crawlerQueue)
        }()
    }()
    
    initDB()
    defer db.Close()
    
    if envPort := os.Getenv("PORT"); envPort != "" {
        port = envPort
    }
    
    links, err := getPDFLinks()
    if err != nil {
        log.Printf("Error getting existing PDF links: %v", err)
    } else {
        safeColorPrintf(colorGreen, "Found %d existing PDF links in database", len(links))
    }
    
    for i := 0; i < numWorkers; i++ {
        workerWg.Add(1)
        go workerRoutine(i + 1)
    }
    
    http.HandleFunc("/pdfs", pdfHandler)
    http.HandleFunc("/start-crawler", startCrawlerHandler)
    
    http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        fmt.Fprintf(w, `
        <!DOCTYPE html>
        <html>
        <head>
            <title>PDF Crawler</title>
            <style>
                body { font-family: Arial, sans-serif; max-width: 800px; margin: 0 auto; padding: 20px; }
                h1 { color: #333; }
                .endpoint { background: #f5f5f5; padding: 10px; margin: 10px 0; border-radius: 5px; }
                pre { background: #eee; padding: 10px; overflow: auto; }
            </style>
        </head>
        <body>
            <h1>PDF Crawler</h1>
            <div class="endpoint">
                <h2>GET /pdfs</h2>
                <p>Returns a list of all found PDF links</p>
            </div>
            <div class="endpoint">
                <h2>POST /start-crawler</h2>
                <p>Starts a new crawler with the specified URL</p>
                <p>Example request:</p>
                <pre>
curl -X POST http://localhost:%s/start-crawler \
  -H "Content-Type: application/json" \
  -d '{"url": "https://example.com"}'
                </pre>
            </div>
        </body>
        </html>
        `, port)
    })
    
    server := &http.Server{
        Addr:         ":" + port,
        ReadTimeout:  10 * time.Second,
        WriteTimeout: 10 * time.Second,
        IdleTimeout:  120 * time.Second,
    }
    
    go func() {
        safeColorPrintf(colorCyan, "Server starting on port %s", port)
        safeColorPrintf(colorCyan, "Endpoints:")
        safeColorPrintf(colorCyan, "  GET  /pdfs           - List all found PDF links")
        safeColorPrintf(colorCyan, "  POST /start-crawler  - Start a new crawler with specified URL")
        safeColorPrintf(colorGreen, "Example: curl -X POST http://localhost:%s/start-crawler -H \"Content-Type: application/json\" -d '{\"url\": \"https://example.com\"}'", port)
        
        if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            log.Fatalf("HTTP server error: %v", err)
        }
    }()
    
    <-ctx.Done()
    
    shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer shutdownCancel()
    
    if err := server.Shutdown(shutdownCtx); err != nil {
        log.Printf("HTTP server shutdown error: %v", err)
    }
    
    workerWgDone := make(chan struct{})
    go func() {
        workerWg.Wait()
        close(workerWgDone)
    }()
    
    select {
    case <-workerWgDone:
        safeColorPrintf(colorGreen, "All workers shut down successfully")
    case <-time.After(15 * time.Second):
        safeColorPrintf(colorYellow, "Timeout waiting for workers to shut down")
    }
    
    safeColorPrintf(colorGreen, "Server shutdown complete")
}