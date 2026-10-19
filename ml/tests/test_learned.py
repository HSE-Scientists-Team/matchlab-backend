import json
import math
import os
from copy import deepcopy
from pathlib import Path
import tempfile
import unittest
from unittest.mock import MagicMock, patch

from matchlab_ml import demo, tracking
from matchlab_ml.learned import LearnedModel, curve_svg, people_events
from matchlab_ml.rank import score_snapshot


class LearnedTests(unittest.TestCase):
    def test_training_has_finite_losses_and_improves_on_synthetic_data(self):
        model = LearnedModel(demo.snapshot())
        self.assertEqual(model.metadata['training_status'], 'trained')
        self.assertEqual(len(model.history), 81)
        for key in ('train_loss', 'validation_loss'):
            self.assertTrue(all(math.isfinite(row[key]) for row in model.history))
            self.assertLess(model.history[-1][key], model.history[0][key])
        self.assertIn('<svg', curve_svg(model.history))

    def test_future_validation_labels_do_not_change_fitted_weights(self):
        source = demo.snapshot()
        first = LearnedModel(source)
        cutoff = first.metadata['validation_cutoff']
        changed = deepcopy(source)
        # Alter only future targets, retaining timestamps and the number of unique events.
        for e in changed['requests']:
            if e['created_at'] > cutoff:
                e['status'] = 'rejected'
        second = LearnedModel(changed)
        self.assertEqual(first.weights, second.weights)
        self.assertEqual(first.history[0]['train_loss'], second.history[0]['train_loss'])
        # A direct change to validation labels leaves train rows/weights unchanged.
        last = max(changed['requests'], key=lambda e:(e['created_at'], e['id']))
        last['candidate_user_id'] = demo.identifier(1)
        third = LearnedModel(changed)
        self.assertEqual(first.weights, third.weights)

    def test_no_events_is_explicit_and_falls_back(self):
        source = demo.snapshot()
        source['requests'] = []
        results, artifact = score_snapshot(source, 'learned')
        self.assertEqual(artifact['training_status'], 'insufficient_data')
        self.assertEqual(artifact['history'], [])
        self.assertEqual(artifact['people_ranker']['training_status'],'insufficient_data')
        self.assertEqual(artifact['people_ranker']['history'],[])
        self.assertIsNone(artifact['ranking_metrics']['sampled_recall_at_10'])
        self.assertTrue(results)

    def test_tracking_logs_steps_plot_and_no_identifiers(self):
        source = demo.snapshot()
        model = LearnedModel(source)
        mlflow = MagicMock()
        with tempfile.TemporaryDirectory() as root:
            directory = Path(root)
            (directory/'learned-model.json').write_text(json.dumps(model.artifact()))
            manifest = {'source_mode':'synthetic','dataset_hash':'test','users':24,'projects':12}
            with patch.dict(os.environ, MLFLOW_TRACKING_URI='http://local.invalid'), patch.dict('sys.modules',mlflow=mlflow):
                tracking.log('test','learned',manifest,{'evaluation_type':'proxy'},directory)
        stepped = [c for c in mlflow.log_metrics.call_args_list if 'step' in c.kwargs]
        self.assertEqual(len(stepped),162)
        self.assertEqual(stepped[-1].kwargs['step'],80)
        self.assertEqual({c.args[1] for c in mlflow.log_text.call_args_list},
                         {'loss-curve.svg','people-loss-curve.svg'})
        self.assertEqual(sum('people_train_loss' in c.args[0] for c in stepped),81)
        payload = json.dumps(model.artifact())
        for user in source['users']:
            self.assertNotIn(user['id'],payload)


    def test_people_training_improves_loss_and_scores(self):
        source = demo.snapshot()
        model = LearnedModel(source)
        people = model.people_ranker
        self.assertEqual(people.metadata['training_status'], 'trained')
        self.assertEqual(people.metadata['label_signal'], 'shared_project_applications')
        for key in ('train_loss','validation_loss'):
            self.assertLess(people.history[-1][key],people.history[0][key])
        viewer = source['users'][1]
        similar = source['users'][5]
        other = source['users'][2]
        self.assertGreater(model.people(viewer,similar)['score'],model.people(viewer,other)['score'])
        self.assertIn('learned_shared_project_interests',model.people(viewer,similar)['factors'])

    def test_people_invitation_and_status_changes_are_not_labels(self):
        source = demo.snapshot()
        source['requests'] = [dict(e,type='invitation') for e in source['requests']]
        self.assertEqual(people_events(source),[])
        model = LearnedModel(source)
        self.assertEqual(model.people_ranker.metadata['training_status'],'insufficient_data')
        source = demo.snapshot()
        first = people_events(source)
        for e in source['requests']:
            e['status'] = 'rejected'
        self.assertEqual(first,people_events(source))

    def test_people_validation_targets_do_not_affect_training(self):
        source = demo.snapshot()
        first = LearnedModel(source).people_ranker
        cutoff = first.metadata['validation_cutoff']
        import matchlab_ml.learned as learned
        events = {}
        for e in people_events(source):
            events.setdefault((e['candidate_user_id'],e['project_id']),e)
        changed = deepcopy(list(events.values()))
        changed[-1]['candidate_user_id'] = 'heldout-only-user'
        # Keep the predefined cutoff stable while changing held-out labels.
        with patch.object(learned,'people_events',return_value=changed):
            second = learned.PairwiseRanker(source,kind='people')
        self.assertEqual(first.metadata['validation_cutoff'],second.metadata['validation_cutoff'])
        self.assertEqual(first.weights,second.weights)
