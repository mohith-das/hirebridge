ALTER TABLE introduction_requests ADD COLUMN recruiter_name    TEXT NOT NULL DEFAULT '';
ALTER TABLE introduction_requests ADD COLUMN recruiter_email   TEXT NOT NULL DEFAULT '';
ALTER TABLE introduction_requests ADD COLUMN recruiter_company TEXT;
ALTER TABLE introduction_requests ADD COLUMN attempts         INTEGER NOT NULL DEFAULT 0;
ALTER TABLE introduction_requests ADD COLUMN last_error        TEXT;
ALTER TABLE introduction_requests ADD COLUMN next_attempt_at   INTEGER;