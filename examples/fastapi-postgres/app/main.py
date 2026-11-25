"""A tiny FastAPI + Postgres app: a list of notes.

The schema is created by `python -m app.migrate`, which forge runs as a
job before every rollout. The app itself only reads and writes rows.
"""

import os

import psycopg
from fastapi import FastAPI
from pydantic import BaseModel

DATABASE_URL = os.environ.get("DATABASE_URL", "postgresql://postgres:postgres@localhost:5432/notes")

app = FastAPI(title="notes")


class NoteIn(BaseModel):
    body: str


def connect():
    return psycopg.connect(DATABASE_URL, autocommit=True)


@app.get("/healthz")
def healthz():
    with connect() as conn:
        conn.execute("SELECT 1")
    return {"ok": True}


@app.get("/notes")
def list_notes():
    with connect() as conn:
        rows = conn.execute("SELECT id, body, created_at FROM notes ORDER BY id DESC LIMIT 100").fetchall()
    return [{"id": r[0], "body": r[1], "created_at": r[2].isoformat()} for r in rows]


@app.post("/notes", status_code=201)
def create_note(note: NoteIn):
    with connect() as conn:
        row = conn.execute("INSERT INTO notes (body) VALUES (%s) RETURNING id", (note.body,)).fetchone()
    return {"id": row[0], "body": note.body}
