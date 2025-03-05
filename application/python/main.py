import asyncio
import uvicorn

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware
from sqlalchemy.ext.asyncio import create_async_engine, AsyncSession
from sqlalchemy.orm import sessionmaker, declarative_base


from api.endpoints import security

# Initialize FastAPI app
app = FastAPI(
    title="Public SOS - Documents API",
    description="Discover and process official documents ",
    terms_of_service="https://publicsos.org",
    contact={
        "name": "Stefan Bogdanel",
        "url": "https://izdrail.com/",
        "email": "stefan@izdrail.com",
    },
    license_info={
        "name": "CC BY 4.0",
        "url": "https://www.apache.org/licenses/LICENSE-2.0.html",
    }
)

# Middleware for CORS
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_methods=["*"],
    allow_headers=["*"],
    allow_credentials=True,
)


import asyncio
import uvicorn
from fastapi import FastAPI, Request
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import HTMLResponse
from fastapi.templating import Jinja2Templates
from fastapi.staticfiles import StaticFiles

from sqlalchemy.ext.asyncio import create_async_engine, AsyncSession
from sqlalchemy.orm import sessionmaker, declarative_base
from fastapi_versioning import VersionedFastAPI, version

from api.endpoints import security
from api.endpoints import nlp



# Router inclusion
app.include_router(security.router)
app.include_router(nlp.router)

app = VersionedFastAPI(app,version_format='{major}')

@app.get("/")
async def root():
    return {"message": "Welcome to the Documents."}

if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=1121, reload=True)