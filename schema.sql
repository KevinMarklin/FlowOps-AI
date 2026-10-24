CREATE TABLE IF NOT EXISTS tickets (
    id SERIAL PRIMARY KEY,
    user_email VARCHAR(255),
    raw_text TEXT NOT NULL,
    department VARCHAR(100) DEFAULT 'Unassigned',
    priority VARCHAR(50) DEFAULT 'medium',
    extracted_summary TEXT,
    rag_answer TEXT,
    operator_reply TEXT,
    status VARCHAR(50) DEFAULT 'new',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
