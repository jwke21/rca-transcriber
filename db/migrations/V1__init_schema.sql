CREATE TABLE engineers (
    id           INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    phone_number TEXT NOT NULL UNIQUE -- E.164, e.g. +15555550123
);

CREATE TABLE incidents (
    id              INTEGER PRIMARY KEY, -- the incident number the SRE enters; not generated
    status          TEXT NOT NULL DEFAULT 'open'
                    CHECK (status IN ('open', 'rca_pending', 'rca_generating', 'rca_complete', 'rca_failed')),
    resolution_time TIMESTAMPTZ,         -- set on the first confirmed resolution; drives the RCA filename
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE incident_events (
    id            INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transcription TEXT NOT NULL,
    incident_id   INTEGER NOT NULL REFERENCES incidents (id),
    engineer_id   INTEGER NOT NULL REFERENCES engineers (id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now() -- when the words were spoken
);

CREATE INDEX incident_events_incident_id_created_at_idx
    ON incident_events (incident_id, created_at, id);
