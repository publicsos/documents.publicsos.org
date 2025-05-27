package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gocolly/colly/v2"
	"github.com/ledongthuc/pdf"

	"github.com/henomis/lingoose/assistant"
	"github.com/henomis/lingoose/embedder/openai"
	"github.com/henomis/lingoose/index"
	"github.com/henomis/lingoose/index/vectordb/jsondb"
	llmopenai "github.com/henomis/lingoose/llm/openai"
	"github.com/henomis/lingoose/rag"
	"github.com/henomis/lingoose/thread"
)

var (
	pdfLinks   = make(chan string, 100)
	visitedPDF = make(map[string]bool)
	wg         sync.WaitGroup
	ragModel   *rag.RAG
	indexMutex sync.Mutex
	statusMsg  = "Idle"
)

func initRAG() {
	idx := index.New(
		jsondb.New().WithPersist("index.json"),
		openai.New(openai.AdaEmbeddingV2),
	)

	ragModel = rag.New(idx).WithTopK(3)
}

func crawlPDFs(startURL string) {
	statusMsg = "Crawling started..."
	c := colly.NewCollector(
		colly.AllowedDomains(strings.TrimPrefix(startURL, "https://")),
	)

	c.OnHTML("a[href]", func(e *colly.HTMLElement) {
		link := e.Request.AbsoluteURL(e.Attr("href"))
		if strings.HasSuffix(link, ".pdf") && !visitedPDF[link] {
			pdfLinks <- link
			visitedPDF[link] = true
		}
	})

	err := c.Visit(startURL)
	if err != nil {
		log.Printf("Failed to visit: %v", err)
	}
	statusMsg = "Crawling complete."
}

func processPDFs() {
	for link := range pdfLinks {
		statusMsg = fmt.Sprintf("Processing: %s", link)
		text, err := extractTextFromPDF(link)
		if err != nil {
			log.Printf("Error extracting PDF: %v", err)
			continue
		}

		indexMutex.Lock()
		err = ragModel.AddSources(context.Background(), text)
		indexMutex.Unlock()

		if err != nil {
			log.Printf("Failed to index PDF: %v", err)
		}
	}
	statusMsg = "Indexing complete."
}

func extractTextFromPDF(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	tmpFile, err := os.CreateTemp("", "*.pdf")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmpFile.Name())

	_, err = io.Copy(tmpFile, resp.Body)
	if err != nil {
		return "", err
	}

	r, err := pdf.Open(tmpFile.Name())
	if err != nil {
		return "", err
	}
	defer r.Close()

	var textBuilder strings.Builder
	totalPage := r.NumPage()
	for i := 1; i <= totalPage; i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		content, _ := p.GetPlainText(nil)
		textBuilder.WriteString(content)
	}

	return textBuilder.String(), nil
}

func handleStartCrawl(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}

	err := json.NewDecoder(r.Body).Decode(&body)
	if err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	go crawlPDFs(body.URL)
	fmt.Fprintln(w, "Started crawling...")
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": statusMsg,
	})
}

func handleQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"query"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	assistant := assistant.New(
		llmopenai.New().WithTemperature(0),
	).WithRAG(ragModel).WithThread(
		thread.New().AddMessages(
			thread.NewUserMessage().AddContent(
				thread.NewTextContent(req.Query),
			),
		),
	)

	if err := assistant.Run(context.Background()); err != nil {
		http.Error(w, "Failed to process query", http.StatusInternalServerError)
		return
	}

	response := assistant.Thread().Messages[len(assistant.Thread().Messages)-1].Content
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"response": response})
}

func handlePDFLinks(w http.ResponseWriter, r *http.Request) {
	var links []string
	for k := range visitedPDF {
		links = append(links, k)
	}
	json.NewEncoder(w).Encode(links)
}

func main() {
	initRAG()

	go processPDFs()

	http.HandleFunc("/start-crawl", handleStartCrawl)
	http.HandleFunc("/status", handleStatus)
	http.HandleFunc("/query", handleQuery)
	http.HandleFunc("/pdf-links", handlePDFLinks)

	log.Println("🔥 Server running on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
