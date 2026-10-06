-- Balancing service schema for MySQL 8.0.
-- All vector-rich payloads (plane/point layout, conventions, run records and
-- event bodies) are stored as JSON; computed state is never stored, it is
-- derived by replaying job_events.

CREATE TABLE IF NOT EXISTS machines (
  id                          VARCHAR(40)  NOT NULL PRIMARY KEY,
  name                        VARCHAR(200) NOT NULL,
  planes                      JSON         NOT NULL,
  points                      JSON         NOT NULL,
  speeds                      JSON         NOT NULL,
  convention                  JSON         NOT NULL,
  reading_change_floor_um     DOUBLE       NOT NULL DEFAULT 0,
  created_at                  DATETIME(6)  NOT NULL,
  updated_at                  DATETIME(6)  NOT NULL
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

CREATE TABLE IF NOT EXISTS jobs (
  id                          VARCHAR(40)  NOT NULL PRIMARY KEY,
  machine_id                  VARCHAR(40)  NOT NULL,
  name                        VARCHAR(200) NOT NULL,
  snapshot                    JSON         NOT NULL,
  use_history                 TINYINT(1)   NOT NULL DEFAULT 0,
  created_at                  DATETIME(6)  NOT NULL,
  updated_at                  DATETIME(6)  NOT NULL,
  KEY idx_jobs_machine (machine_id),
  CONSTRAINT fk_jobs_machine FOREIGN KEY (machine_id) REFERENCES machines (id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- Per-job monotonic sequence counter. A single UPDATE ... LAST_INSERT_ID()
-- inside the writer transaction atomically takes the next seq, so ordering is
-- gap-free per job even under concurrent submissions, while staying fully
-- transactional (unlike an AUTO_INCREMENT side table).
CREATE TABLE IF NOT EXISTS job_event_seq (
  job_id                      VARCHAR(40)  NOT NULL PRIMARY KEY,
  last_seq                    BIGINT       NOT NULL DEFAULT 0,
  CONSTRAINT fk_seq_job FOREIGN KEY (job_id) REFERENCES jobs (id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

CREATE TABLE IF NOT EXISTS job_events (
  job_id                      VARCHAR(40)  NOT NULL,
  seq                         BIGINT       NOT NULL,
  type                        VARCHAR(40)  NOT NULL,
  run_id                      VARCHAR(64)  NOT NULL DEFAULT '',
  client_id                   VARCHAR(128) NULL,
  payload                     JSON         NOT NULL,
  occurred_at                 DATETIME(6)  NOT NULL,
  created_at                  DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (job_id, seq),
  UNIQUE KEY uq_events_client (job_id, client_id),
  KEY idx_events_type_run (job_id, type, run_id),
  CONSTRAINT fk_events_job FOREIGN KEY (job_id) REFERENCES jobs (id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- One finalized coefficient contribution per job and speed.
CREATE TABLE IF NOT EXISTS job_contributions (
  job_id                      VARCHAR(40)  NOT NULL,
  machine_id                  VARCHAR(40)  NOT NULL,
  speed                       INT          NOT NULL,
  plane_ids                   JSON         NOT NULL,
  point_ids                   JSON         NOT NULL,
  re                          JSON         NOT NULL,
  im                          JSON         NOT NULL,
  uncert                      JSON         NOT NULL,
  trust_ok                    TINYINT(1)   NOT NULL DEFAULT 1,
  updated_at                  DATETIME(6)  NOT NULL,
  PRIMARY KEY (job_id, speed),
  KEY idx_contrib_machine (machine_id, speed, updated_at)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- Current fused coefficient (the value reused by later jobs). Rebuilt from
-- job_contributions whenever a job finalizes or a finalized job is corrected,
-- so correcting an old run updates machine history exactly as if it had been
-- entered correctly.
CREATE TABLE IF NOT EXISTS coeff_current (
  machine_id                  VARCHAR(40)  NOT NULL,
  speed                       INT          NOT NULL,
  plane_ids                   JSON         NOT NULL,
  point_ids                   JSON         NOT NULL,
  re                          JSON         NOT NULL,
  im                          JSON         NOT NULL,
  uncert                      JSON         NOT NULL,
  n_contrib                   INT          NOT NULL DEFAULT 0,
  status                      VARCHAR(20)  NOT NULL DEFAULT 'active',
  reason                      VARCHAR(500) NOT NULL DEFAULT '',
  created_at                  DATETIME(6)  NOT NULL,
  updated_at                  DATETIME(6)  NOT NULL,
  PRIMARY KEY (machine_id, speed)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;
