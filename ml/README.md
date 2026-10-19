# MatchLab batch models

Python 3.10+. The ranking core needs only the standard library; PostgreSQL,
Redis and MLflow libraries are imported only at the integration boundary.

```sh
PYTHONPATH=ml python3 -m unittest discover -s ml/tests -v
PYTHONPATH=ml python3 -m matchlab_ml --source demo --offline --artifact-dir /tmp/matchlab-ml-demo
```

For a connected batch, install `pip install './ml[tracking]'`, configure
`SOURCE_DATABASE_URL`, `ANALYTICS_DATABASE_URL`, `RECOMMENDATION_REDIS_URL`
and `MLFLOW_TRACKING_URI`, then run `python3 -m matchlab_ml`.
Source defaults to PostgreSQL, serving model to baseline. All three models are always
fitted/scored/evaluated. `--model hybrid` selects which results are published;
it does not imply hybrid has been shown to be better.

`--source demo` is explicitly synthetic. `--offline` skips analytics/Redis, but
still logs to MLflow if a tracking URI is set. Publishing synthetic recommendations
requires the additional explicit `ML_ALLOW_DEMO_PUBLISH=true` guard. Never enable
that flag for a real environment. Demo UUIDs do not create backend accounts.
`--analytics-only` can fill explicitly labelled synthetic BI data without publishing
any recommendation IDs; the local launcher exposes this as `demo-analytics`.

Modules are ordinary functions, shared by CLI and Airflow:

| Module | Responsibility |
| --- | --- |
| `extract` | Read-only consistent owner exports, strict field contracts, private JSON snapshots |
| `features` | Tags/aliases, eligibility, inverted candidate indexes, sparse TF-IDF |
| `baseline` | Explainable weighted tag rules |
| `hybrid` | Rules + learned TF-IDF + application-neighbour similarity |
| `learned` | Pairwise logistic project ranker with temporal validation and per-epoch loss |
| `rank` | Candidate pool, filters, ordering, stable daily cold start |
| `evaluate` | Retrospective next-application proxy, explicit unknown metrics |
| `cache` | Versioned Redis writes, TTL and stale-generation-safe promotion |
| `analytics` / `analytics.sql` | Separate minimized snapshots and twelve BI views |
| `segments` | Published skill taxonomy, including unknown; not employment data |
| `tracking` | Safe run lineage/parameters/metrics to MLflow; no raw profile vocabulary |
| `pipeline` | Four stage functions used by both entrypoints |
| `provision` | Explicit owner-view/analytics grants and metadata DB provisioning |
| `maintenance` | Retention preview/apply and source/cache freshness exit status |

Model weights are release hypotheses, not calibrated probabilities. User-user
matching is similarity-based, not a claim of complementary roles or personal
compatibility. Candidate pools are bounded for a small pilot, not an ANN service.
Historical profiles, impressions and negative-feedback events are not available;
the evaluation is not an unbiased online performance estimate.

Snapshot/model/cache-list files are permission-restricted but still contain
pseudonymous or potentially personal data. Full fitted vocabulary stays local;
MLflow receives only a vocabulary fingerprint/size. Local artifacts and analytics
history persist until explicitly managed. `python -m matchlab_ml.maintenance` previews
7-day raw / 30-day analytics cleanup; `--apply` performs it in a maintenance window
with scheduling paused. The active cache run and latest completed analytics run
are protected. `--check-freshness` returns nonzero for stale/missing real source or
cache. Historical erasure, tracking metadata and backup retention need their own
operator procedure; see the infrastructure pilot documentation.

The local orchestration/BI overlay is maintained in `matchlab-infra/compose/ml`.
Go serves existing HTTP/gRPC routes, never imports this Python package and never
reads owner business tables for recommendations.

## Trainable ranker

`learned` learns three project-feature weights (skill coverage, topic overlap,
preferred project type) with pairwise logistic loss and L2 regularization.
Applications are weak positive intent; sampled unobserved projects are not dislikes.
People recommendations have a separate pairwise ranker: skill overlap, interest
overlap and matching specialization. Its weak positive labels come from users
applying to the same project, timestamped when the second application occurs.
Repeated applications contribute no new labels; each action adds at most 20 peers
in both directions. Invitations and application outcomes are not labels. This
learns shared project interests, not mutual consent or complementary roles.
Current bilateral blocks, private profiles and eligibility filters remain enforced.
No pretrained model download or GPU is needed.

A global chronological 80/20 application split keeps validation actions out of
training. Current profiles, role availability and visibility are still retrospective
proxies. Repeated user/project applications are deduplicated. Negatives are bounded
to 20 per positive and must have existed at event time. Training excludes known
training positives; validation excludes all known positives. At least five training
pairs and one validation pair are required. Otherwise `training_status` is
`insufficient_data`, no loss is fabricated, and scoring uses baseline rules.

MLflow logs `train_loss` and `validation_loss` at steps 0–80, `loss-history.json`,
`loss-curve.svg`, and `learned-parameters.json` with learned weights. People have
separate `people_train_loss`/`people_validation_loss`, `people-loss-history.json`,
`people-loss-curve.svg` and a nested `people_ranker` parameter object. Training
status, split and sufficiency are independent for the two rankers. People sampled
quality metrics use the `people_sampled_` prefix. `sampled_recall_at_10` and `sampled_ndcg_at_10`
use sampled validation candidates and are distinct from the common full-pool
`recall_at_10`/`ndcg_at_10` evaluation shared with baseline/hybrid. Loss measures pairwise preference fitting,
not a calibrated probability or proof of product improvement.

For a clearly labelled synthetic training demonstration without changing BI or cache:

```sh
python -m matchlab_ml --source demo --offline --artifact-dir /tmp/matchlab-training-demo
```

Real batches train only when actual application history is sufficient. The serving
model stays baseline unless explicitly selected with `--model learned` or `ML_MODEL`.
