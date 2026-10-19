"""Small deterministic pairwise logistic ranker; application intent is a weak label.

Global chronological holdout. Current profile/catalogue features make validation
retrospective, not a historical replay. Unobserved projects are sampled negatives,
not dislikes. People use co-application shared-interest labels, not consent.
No user/project identifiers or free text are model parameters.
"""
import hashlib
import math

from . import baseline
from .features import coverage, overlap, open_roles, tags, active_user, indexes, eligible_project, eligible_person

FEATURES = ['skill_coverage', 'topic_overlap', 'preferred_type']
PEOPLE_FEATURES = ['skill_overlap', 'interest_overlap', 'same_specialization']


def people_features(user, candidate):
    return [overlap(user.get('skills'), candidate.get('skills')),
            overlap(user.get('interests'), candidate.get('interests')),
            float(bool(user.get('specialization')) and
                  user['specialization'].casefold() == candidate.get('specialization', '').casefold())]


def people_events(snapshot):
    """A weak shared-interest proxy, never invitations, acceptance or dislikes."""
    previous = {}
    result = []
    events = sorted((e for e in snapshot.get('requests', []) if e['type'] == 'application'),
                    key=lambda e: (e['created_at'], e['id']))
    for event in events:
        peers = previous.setdefault(event['project_id'], set())
        user = event['candidate_user_id']
        if user in peers:
            continue
        # Bound the number of pair labels contributed by each application.
        ordered = sorted(peers, key=lambda other: hashlib.sha256((event['id']+other).encode()).digest())[:20]
        for other in ordered:
            for viewer, candidate in ((user, other), (other, user)):
                result.append({'id':event['id']+viewer+candidate, 'candidate_user_id':viewer,
                               'project_id':candidate, 'created_at':event['created_at']})
        peers.add(user)
    return result


def features(user, project):
    return [max((coverage(user.get('skills'), r.get('skills')) for r in open_roles(project)), default=0),
            overlap(user.get('interests'), (project.get('tags') or []) + [project.get('field', '')]),
            float(project.get('type') in (user.get('project_types') or []))]


def sigmoid(value):
    return 1 / (1 + math.exp(-max(-40, min(40, value))))


def loss(rows, weights):
    return sum(max(-sum(w*x for w,x in zip(weights,row)), 0) +
               math.log1p(math.exp(-abs(sum(w*x for w,x in zip(weights,row)))))
               for row in rows) / len(rows) if rows else None


class PairwiseRanker:
    def __init__(self, snapshot, epochs=80, learning_rate=0.5, kind="projects"):
        self.kind = kind
        self.feature_fn = people_features if kind == "people" else features
        names = PEOPLE_FEATURES if kind == "people" else FEATURES
        self.weights = [0.0] * len(names)
        self.history = []
        data = indexes(snapshot)
        events = people_events(snapshot) if kind == 'people' else sorted(
            (e for e in snapshot.get('requests', []) if e['type'] == 'application'),
            key=lambda e: (e['created_at'], e['id']))
        # Deduplicate repeated applications to the same project.
        unique = {}
        for event in events:
            unique.setdefault((event['candidate_user_id'], event['project_id']), event)
        events = list(unique.values())
        cutoff = events[int(len(events)*0.8)]['created_at'] if len(events) >= 5 else None
        train_events = [e for e in events if cutoff and e['created_at'] < cutoff]
        validation = [e for e in events if cutoff and e['created_at'] >= cutoff]
        known_train = {(e['candidate_user_id'],e['project_id']) for e in train_events}
        known_all = set(unique)

        def pairs(selected, known):
            rows, groups = [], []
            evaluated_users = set()
            for event in selected:
                user = data['users'].get(event['candidate_user_id'])
                catalogue = data['users'] if kind == 'people' else data['projects']
                eligible = eligible_person if kind == 'people' else eligible_project
                positive = catalogue.get(event['project_id'])
                if not user or not active_user(user) or not positive:
                    continue
                # Historical membership/availability cannot be reconstructed. Keep
                # privacy/owner restrictions; use current recruiting catalogue proxy.
                if not eligible(user, positive, {**data, 'memberships': set()}):
                    continue
                candidates = [p for p in catalogue.values()
                              if (user['id'],p['id']) not in known
                              and p.get('created_at','') <= event['created_at']
                              and eligible(user,p,{**data,'memberships':set()})]
                candidates.sort(key=lambda p: hashlib.sha256((event['id']+p['id']).encode()).digest())
                pos = self.feature_fn(user,positive)
                negatives = [self.feature_fn(user,p) for p in candidates[:20]]
                if negatives:
                    rows.extend([[a-b for a,b in zip(pos,neg)] for neg in negatives])
                    groups.append((pos,negatives))
                    evaluated_users.add(user["id"])
            return rows, groups, len(evaluated_users)

        train, _, _ = pairs(train_events, known_train)
        valid, groups, user_count = pairs(validation, known_all)
        self.metadata = {'version':'learned-people-v1' if kind == 'people' else 'learned-v1', 'algorithm':'pairwise_logistic',
                         'kind':kind, 'features':names, 'training_status':'insufficient_data',
                         'training_pairs':len(train), 'validation_pairs':len(valid),
                         'validation_cutoff':cutoff, 'negative_sampling':'unobserved_not_dislikes',
                         'label_signal':'shared_project_applications' if kind == 'people' else 'application_creation', 'evaluation_type':'temporal_current_profile_sampled_proxy'}
        self.metrics = {'sampled_validation_users':user_count, 'sampled_validation_queries':len(groups),
                        'sampled_recall_at_10':None, 'sampled_ndcg_at_10':None}
        if len(train) < 5 or not valid:
            return
        for epoch in range(epochs+1):
            self.history.append({'epoch':epoch, 'train_loss':loss(train,self.weights),
                                 'validation_loss':loss(valid,self.weights)})
            if epoch == epochs:
                break
            gradient = [0.0]*len(names)
            for row in train:
                error = sigmoid(sum(w*x for w,x in zip(self.weights,row))) - 1
                for j,x in enumerate(row):
                    gradient[j] += error*x/len(train)
            self.weights = [w-learning_rate*(g+0.001*w) for w,g in zip(self.weights,gradient)]
        self.metadata['training_status'] = 'trained'
        self.metadata.update(epochs=epochs, learning_rate=learning_rate, l2=0.001)
        ranks = [1+sum(sum(w*x for w,x in zip(self.weights,neg)) >=
                       sum(w*x for w,x in zip(self.weights,pos)) for neg in negatives)
                 for pos,negatives in groups]
        self.metrics.update(sampled_recall_at_10=sum(r<=10 for r in ranks)/len(ranks),
                            sampled_ndcg_at_10=sum(1/math.log2(r+1) if r<=10 else 0 for r in ranks)/len(ranks))

    def score(self, viewer, candidate):
        result = baseline.people(viewer,candidate) if self.kind == 'people' else baseline.project(viewer,candidate)
        values = self.feature_fn(viewer,candidate)
        if self.metadata['training_status'] == 'trained' and any(values):
            result['score'] = sigmoid(sum(w*x for w,x in zip(self.weights,values)))
            result['factors'].append('learned_shared_project_interests' if self.kind == 'people' else 'learned_application_preferences')
        return result

    def artifact(self):
        return {**self.metadata, 'weights':self.weights, 'history':self.history,
                'ranking_metrics':self.metrics}


class LearnedModel(PairwiseRanker):
    def __init__(self, snapshot, epochs=80, learning_rate=0.5):
        super().__init__(snapshot, epochs, learning_rate)
        self.people_ranker = PairwiseRanker(snapshot, epochs, learning_rate, kind="people")
        self.metadata['people_model'] = 'learned-people-v1'
        self.metrics.update({'people_'+key:value for key,value in self.people_ranker.metrics.items()})

    def people(self, viewer, candidate):
        return self.people_ranker.score(viewer,candidate)

    def project(self, viewer, candidate):
        return self.score(viewer,candidate)

    def artifact(self):
        return {**super().artifact(), 'people_ranker':self.people_ranker.artifact()}


def curve_svg(history):
    """Standalone plot without pulling a plotting stack into the worker image."""
    values = [r[k] for r in history for k in ('train_loss','validation_loss')]
    lo, hi = min(values), max(values)
    lines = []
    for key,color in [('train_loss','#2563eb'),('validation_loss','#ef4444')]:
        points = ' '.join(f'{60+600*i/max(len(history)-1,1):.2f},{330-270*(r[key]-lo)/max(hi-lo,1e-9):.2f}' for i,r in enumerate(history))
        lines.append(f'<polyline fill="none" stroke="{color}" stroke-width="2" points="{points}"/>')
    return ('<svg xmlns="http://www.w3.org/2000/svg" width="740" height="400">'
            '<rect width="100%" height="100%" fill="white"/>'
            '<g font-family="sans-serif" font-size="14">'
            '<text x="60" y="25">Pairwise logistic loss (sampled negatives)</text>'
            f'<text x="10" y="65">{hi:.3f}</text><text x="10" y="335">{lo:.3f}</text>'
            '<path d="M60 50V330H670" fill="none" stroke="black"/>' + ''.join(lines) +
            '<text x="60" y="355">0</text>' + f'<text x="640" y="355">{history[-1]["epoch"]}</text>' +
            '<text x="320" y="380">Epoch</text><text x="400" y="25" fill="#2563eb">Train</text>'
            '<text x="480" y="25" fill="#ef4444">Validation</text></g></svg>')
