CREATE SCHEMA IF NOT EXISTS analytics;
CREATE TABLE IF NOT EXISTS analytics.runs (
    run_id text PRIMARY KEY, extracted_at timestamptz NOT NULL,
    data_source text NOT NULL, model text NOT NULL, state text NOT NULL,
    dataset_hash text NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS analytics.users_snapshot (
    run_id text NOT NULL REFERENCES analytics.runs(run_id), id text NOT NULL,
    status text NOT NULL, system_role text NOT NULL, confirmed boolean NOT NULL,
    registered_at timestamptz NOT NULL, last_login_at timestamptz,
    university text NOT NULL, study_level text NOT NULL, open_to_collaboration boolean NOT NULL,
    segment text NOT NULL, skills jsonb NOT NULL, PRIMARY KEY(run_id,id)
);
CREATE TABLE IF NOT EXISTS analytics.projects_snapshot (
    run_id text NOT NULL REFERENCES analytics.runs(run_id), id text NOT NULL,
    type text NOT NULL, field text NOT NULL, status text NOT NULL, hidden boolean NOT NULL,
    created_at timestamptz NOT NULL, PRIMARY KEY(run_id,id)
);
CREATE TABLE IF NOT EXISTS analytics.roles_snapshot (
    run_id text NOT NULL REFERENCES analytics.runs(run_id), id text NOT NULL,
    project_id text NOT NULL, title text NOT NULL, slots integer NOT NULL,
    occupied integer NOT NULL, skills jsonb NOT NULL, PRIMARY KEY(run_id,id)
);
CREATE TABLE IF NOT EXISTS analytics.requests_snapshot (
    run_id text NOT NULL REFERENCES analytics.runs(run_id), id text NOT NULL,
    type text NOT NULL, status text NOT NULL, created_at timestamptz NOT NULL,
    PRIMARY KEY(run_id,id)
);
CREATE TABLE IF NOT EXISTS analytics.model_metrics (
    run_id text NOT NULL REFERENCES analytics.runs(run_id), model text NOT NULL,
    generated_users integer NOT NULL, recommendations integer NOT NULL,
    fallback_fraction double precision NOT NULL, duration_seconds double precision NOT NULL,
    evaluation_users integer NOT NULL, recall_at_10 double precision, ndcg_at_10 double precision,
    PRIMARY KEY(run_id,model)
);
CREATE OR REPLACE VIEW analytics.latest_run AS
SELECT * FROM analytics.runs WHERE state='complete'
ORDER BY extracted_at DESC,created_at DESC,run_id DESC LIMIT 1;

CREATE OR REPLACE VIEW analytics.overview AS
SELECT l.extracted_at AS as_of,l.data_source,
 (SELECT count(*) FROM analytics.users_snapshot u WHERE u.run_id=l.run_id) AS users,
 (SELECT count(*) FROM analytics.users_snapshot u WHERE u.run_id=l.run_id AND u.status='active' AND u.confirmed) AS active_users,
 (SELECT count(*) FROM analytics.projects_snapshot p WHERE p.run_id=l.run_id) AS projects,
 (SELECT count(*) FROM analytics.projects_snapshot p WHERE p.run_id=l.run_id AND p.status='recruiting' AND NOT p.hidden) AS recruiting_projects,
 (SELECT coalesce(sum(greatest(r.slots-r.occupied,0)),0) FROM analytics.roles_snapshot r
  JOIN analytics.projects_snapshot p ON p.run_id=r.run_id AND p.id=r.project_id
  WHERE r.run_id=l.run_id AND p.status='recruiting' AND NOT p.hidden) AS open_places
FROM analytics.latest_run l;

CREATE OR REPLACE VIEW analytics.user_segments AS
SELECT u.segment,count(*) AS users,l.extracted_at AS as_of,l.data_source
FROM analytics.users_snapshot u JOIN analytics.latest_run l USING(run_id) GROUP BY u.segment,l.extracted_at,l.data_source;
CREATE OR REPLACE VIEW analytics.universities AS
SELECT coalesce(nullif(u.university,''),'Unknown') AS university,count(*) AS users,l.data_source
FROM analytics.users_snapshot u JOIN analytics.latest_run l USING(run_id) GROUP BY university,l.data_source;
CREATE OR REPLACE VIEW analytics.skill_supply AS
SELECT skill,count(DISTINCT u.id) AS users,l.data_source
FROM analytics.users_snapshot u JOIN analytics.latest_run l USING(run_id),
LATERAL jsonb_array_elements_text(u.skills) AS s(skill)
WHERE u.status='active' AND u.confirmed AND u.open_to_collaboration GROUP BY skill,l.data_source;
CREATE OR REPLACE VIEW analytics.registrations AS
SELECT u.registered_at::date AS day,count(*) AS registrations,l.data_source
FROM analytics.users_snapshot u JOIN analytics.latest_run l USING(run_id) GROUP BY day,l.data_source;
CREATE OR REPLACE VIEW analytics.project_status AS
SELECT p.status,count(*) AS projects,l.data_source FROM analytics.projects_snapshot p
JOIN analytics.latest_run l USING(run_id) GROUP BY p.status,l.data_source;
CREATE OR REPLACE VIEW analytics.project_types AS
SELECT p.type,count(*) AS projects,l.data_source FROM analytics.projects_snapshot p
JOIN analytics.latest_run l USING(run_id) GROUP BY p.type,l.data_source;
CREATE OR REPLACE VIEW analytics.role_demand AS
SELECT r.title AS role,count(*) FILTER(WHERE r.slots>r.occupied) AS openings,sum(greatest(r.slots-r.occupied,0)) AS open_places,
sum(r.occupied) AS occupied_places,l.data_source
FROM analytics.roles_snapshot r JOIN analytics.projects_snapshot p ON p.run_id=r.run_id AND p.id=r.project_id
JOIN analytics.latest_run l ON l.run_id=r.run_id
WHERE p.status='recruiting' AND NOT p.hidden GROUP BY r.title,l.data_source;
CREATE OR REPLACE VIEW analytics.skill_demand AS
SELECT skill,sum(greatest(r.slots-r.occupied,0)) AS open_places,l.data_source
FROM analytics.roles_snapshot r JOIN analytics.projects_snapshot p ON p.run_id=r.run_id AND p.id=r.project_id
JOIN analytics.latest_run l ON l.run_id=r.run_id,LATERAL jsonb_array_elements_text(r.skills) AS s(skill)
WHERE p.status='recruiting' AND NOT p.hidden GROUP BY skill,l.data_source;
CREATE OR REPLACE VIEW analytics.recruitment AS
SELECT r.type,r.status,count(*) AS requests,l.data_source
FROM analytics.requests_snapshot r JOIN analytics.latest_run l USING(run_id) GROUP BY r.type,r.status,l.data_source;
CREATE OR REPLACE VIEW analytics.team_formation AS
WITH filled AS (
 SELECT p.id,count(r.id) FILTER(WHERE r.occupied>0) AS filled_roles FROM analytics.projects_snapshot p
 JOIN analytics.latest_run l ON l.run_id=p.run_id
 LEFT JOIN analytics.roles_snapshot r ON r.run_id=p.run_id AND r.project_id=p.id GROUP BY p.id
)
SELECT count(*) AS projects,count(*) FILTER(WHERE filled_roles>=2) AS projects_two_filled_roles,
count(*) FILTER(WHERE filled_roles>=2)::double precision/nullif(count(*),0) AS current_two_role_share,
(SELECT data_source FROM analytics.latest_run) AS data_source FROM filled;
CREATE OR REPLACE VIEW analytics.recommendation_quality AS
SELECT m.*,r.extracted_at,r.data_source,r.state FROM analytics.model_metrics m
JOIN analytics.runs r USING(run_id);

-- Grant BI accounts only these aggregate views, not the snapshot tables or source DB.
REVOKE ALL ON ALL TABLES IN SCHEMA analytics FROM PUBLIC;
