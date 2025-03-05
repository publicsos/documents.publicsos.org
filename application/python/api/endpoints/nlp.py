from fastapi import APIRouter, HTTPException, File, UploadFile
from pydantic import BaseModel
import spacy
import os
import pymupdf4llm
from newspaper import Article, Config
from markdownify import markdownify as md
from vaderSentiment.vaderSentiment import SentimentIntensityAnalyzer
import yake
import socials
import socid_extractor
import socialshares
from spacy import displacy
import sqlite3
import json
from datetime import datetime
import logging

# Configure logging
logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

# Initialize FastAPI Router
router = APIRouter()

# Load SpaCy Model
nlp = spacy.load("en_core_web_trf")

# Directories and Database
UPLOAD_DIRECTORY = "pdfs"
DATABASE = "nlp_data.db"
os.makedirs(UPLOAD_DIRECTORY, exist_ok=True)

# Initialize Sentiment Analyzer
sentiment_analyzer = SentimentIntensityAnalyzer()

# Constants
EXCLUDED_ENTITY_TYPES = {}

# Request Models
class ArticleAction(BaseModel):
    link: str

class SummarizeAction(BaseModel):
    text: str

# Database Setup
def init_db():
    conn = sqlite3.connect(DATABASE)
    c = conn.cursor()
    
    # Create tables
    c.execute('''CREATE TABLE IF NOT EXISTS articles (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        link TEXT UNIQUE,
        title TEXT,
        date TEXT,
        text TEXT,
        data JSON,
        created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
    )''')
    
    c.execute('''CREATE TABLE IF NOT EXISTS pdfs (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        filename TEXT UNIQUE,
        markdown TEXT,
        entities JSON,
        created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
    )''')
    
    c.execute('''CREATE TABLE IF NOT EXISTS tags (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        text TEXT,
        keywords JSON,
        created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
    )''')
    
    conn.commit()
    conn.close()

# Initialize database on startup
init_db()

# Helper Functions
def filter_entities(doc):
    return list(dict.fromkeys((ent.label_, ent.text) for ent in doc.ents if ent.label_ not in EXCLUDED_ENTITY_TYPES))

def fetch_article(link: str) -> Article:
    try:
        config = Config()
        config.browser_user_agent = "NLP/0.0.1 (Unix; Intel) Chrome/123.0.0"
        config.request_timeout = 10
        config.fetch_images = True
        config.memoize_articles = True
        config.follow_meta_refresh = True

        article = Article(link, config=config, keep_article_html=True)
        article.download()
        article.parse()
        return article
    except Exception as e:
        raise HTTPException(status_code=400, detail=f"Failed to fetch article: {str(e)}")

def extract_keywords(text: str, language: str = "en", n: int = 1, dedup_lim: float = 0.9, top: int = 5):
    extractor = yake.KeywordExtractor(lan=language, n=n, dedupLim=dedup_lim, top=top)
    return sorted(extractor.extract_keywords(text), key=lambda x: x[1])

def perform_social_analysis(link: str, text: str):
    try:
        return {
            "social_accounts": socials.extract(link).get_matches_per_platform(),
            "social_shares": socialshares.fetch(link, platforms=["facebook", "pinterest", "linkedin", "google", "reddit"]),
            "sentiment": sentiment_analyzer.polarity_scores(text),
            "accounts": socid_extractor.extract(text),
        }
    except Exception as e:
        raise HTTPException(status_code=500, detail=f"Social analysis failed: {str(e)}")

# Endpoints
@router.post("/nlp/article")
async def process_article(article: ArticleAction):
    try:
        fetched_article = fetch_article(article.link)
        doc = nlp(fetched_article.text)
        filtered_entities = filter_entities(doc)
        social_analysis = perform_social_analysis(article.link, fetched_article.text)
        spacy_html = displacy.render(doc, style="ent", options={"ents": [e[0] for e in filtered_entities]})
        keywords = extract_keywords(fetched_article.text, top=5)
        
        response_data = {
            "title": fetched_article.title,
            "date": str(fetched_article.publish_date) if fetched_article.publish_date else None,
            "text": fetched_article.text,
            "markdown": md(fetched_article.article_html, newline_style="BACKSLASH", strip=["a"], heading_style="ATX"),
            "html": fetched_article.article_html,
            "summary": fetched_article.summary,
            "keywords": keywords,
            "authors": fetched_article.authors,
            "banner": fetched_article.top_image,
            "images": list(fetched_article.images),
            "entities": filtered_entities,
            "videos": fetched_article.movies,
            "social": social_analysis["social_accounts"],
            "spacy": spacy_html,
            "spacy_markdown": md(spacy_html, newline_style="BACKSLASH", strip=["a"], heading_style="ATX"),
            "sentiment": social_analysis["sentiment"],
            "accounts": social_analysis["accounts"],
            "social_shares": social_analysis["social_shares"],
        }

        # Save to SQLite
        conn = sqlite3.connect(DATABASE)
        c = conn.cursor()
        c.execute('''INSERT OR REPLACE INTO articles (link, title, date, text, data) 
                    VALUES (?, ?, ?, ?, ?)''', 
                 (article.link, 
                  response_data["title"], 
                  response_data["date"], 
                  response_data["text"], 
                  json.dumps(response_data)))
        conn.commit()
        conn.close()

        return {"data": response_data}
    except Exception as e:
        logger.error(f"Error processing article: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error processing article: {str(e)}")

@router.post("/nlp/tags")
async def extract_tags(action: SummarizeAction):
    try:
        keywords = extract_keywords(action.text, n=3, top=5)
        
        # Save to SQLite
        conn = sqlite3.connect(DATABASE)
        c = conn.cursor()
        c.execute('''INSERT INTO tags (text, keywords) VALUES (?, ?)''', 
                 (action.text, json.dumps(keywords)))
        conn.commit()
        conn.close()

        return {"data": keywords}
    except Exception as e:
        logger.error(f"Keyword extraction failed: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Keyword extraction failed: {str(e)}")

@router.post("/nlp/pdf-reader/")
async def upload_pdf(file: UploadFile = File(...)):
    if file.content_type != "application/pdf" or not file.filename.endswith(".pdf"):
        raise HTTPException(status_code=400, detail="Only PDF files are allowed.")
    try:
        file_path = os.path.join(UPLOAD_DIRECTORY, file.filename)
        with open(file_path, "wb") as f:
            f.write(file.file.read())
        markdown_text = pymupdf4llm.to_markdown(file_path)
        doc = nlp(markdown_text)
        entities = filter_entities(doc)

        # Save to SQLite
        conn = sqlite3.connect(DATABASE)
        c = conn.cursor()
        c.execute('''INSERT OR REPLACE INTO pdfs (filename, markdown, entities) 
                    VALUES (?, ?, ?)''', 
                 (file.filename, markdown_text, json.dumps(entities)))
        conn.commit()
        conn.close()

        return {
            "message": f"Successfully uploaded {file.filename}",
            "markdown": markdown_text,
            "entities": entities
        }
    except Exception as e:
        logger.error(f"Error processing PDF: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error processing the file: {str(e)}")
    finally:
        file.file.close()

# New endpoints to list saved data
@router.get("/nlp/articles")
async def list_articles():
    try:
        conn = sqlite3.connect(DATABASE)
        conn.row_factory = sqlite3.Row
        c = conn.cursor()
        c.execute("SELECT * FROM articles ORDER BY created_at DESC")
        articles = [dict(row) for row in c.fetchall()]
        for article in articles:
            article['data'] = json.loads(article['data'])
        conn.close()
        return {"data": articles}
    except Exception as e:
        logger.error(f"Error listing articles: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error listing articles: {str(e)}")

@router.get("/nlp/tags")
async def list_tags():
    try:
        conn = sqlite3.connect(DATABASE)
        conn.row_factory = sqlite3.Row
        c = conn.cursor()
        c.execute("SELECT * FROM tags ORDER BY created_at DESC")
        tags = [dict(row) for row in c.fetchall()]
        for tag in tags:
            tag['keywords'] = json.loads(tag['keywords'])
        conn.close()
        return {"data": tags}
    except Exception as e:
        logger.error(f"Error listing tags: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error listing tags: {str(e)}")

@router.get("/nlp/pdfs")
async def list_pdfs():
    try:
        conn = sqlite3.connect(DATABASE)
        conn.row_factory = sqlite3.Row
        c = conn.cursor()
        c.execute("SELECT * FROM pdfs ORDER BY created_at DESC")
        pdfs = [dict(row) for row in c.fetchall()]
        for pdf in pdfs:
            pdf['entities'] = json.loads(pdf['entities'])
        conn.close()
        return {"data": pdfs}
    except Exception as e:
        logger.error(f"Error listing PDFs: {str(e)}")
        raise HTTPException(status_code=500, detail=f"Error listing PDFs: {str(e)}")