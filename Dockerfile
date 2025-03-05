# Main application image
FROM python:3.9

# Install required system packages
RUN apt-get update && apt-get install -y --no-install-recommends \
    procps \
    gnupg \
    nodejs \
    npm \
    gosu \
    curl \
    ca-certificates \
    protobuf-compiler \
    zip \
    unzip \
    git \
    supervisor \
    sqlite3 \
    libcap2-bin \
    libpng-dev \
    python3 \
    python3-pip \
    python3.11-venv \
    dnsutils \
    librsvg2-bin \
    fswatch \
    nano \
    cargo \
    ffmpeg \
    poppler-utils \
    libzip-dev \
    libonig-dev \
    libjson-c-dev \
    build-essential \
    autoconf \
    zlib1g-dev \
    pkg-config \
    wget \
    redis \
    golang \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*



# Install Python tools
RUN pip install pipx --break-system-packages
RUN pip install spacy-streamlit --break-system-packages

# Install Python dependencies
WORKDIR /var/www/python/
RUN pip install --no-cache-dir \
    PyMuPDF \
    pymupdf4llm \
    fastapi \
    fastapi_versioning \
    yake \
    vaderSentiment \
    python-multipart \
    markdownify \
    newspaper3k \
    uvicorn \
    duckdb \
    lxml_html_clean \
    sqlalchemy \
    spacy \
    spacy \
    spacy-transformers \
    spacy-streamlit \
    spacy-llm \
    socialshares \
    socid_extractor \
    socials \
    --break-system-packages


COPY ./application/python .

# Install spaCy model
RUN python3 -m spacy download en_core_web_trf --break-system-packages

# Configure Supervisor
COPY ./docker/supervisord.conf /etc/supervisor/conf.d/supervisord.conf


# Expose ports
EXPOSE 1121 1122

# Set working directory back to application root
WORKDIR /var/www/

# Start Supervisor
ENTRYPOINT ["supervisord", "-c", "/etc/supervisor/conf.d/supervisord.conf", "-n"]
