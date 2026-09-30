import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('scoped_sql', Path(__file__).parents[1] / 'scripts/prepare_sql.py')
scoped_sql = importlib.util.module_from_spec(spec)
spec.loader.exec_module(scoped_sql)


class ScopedSQLTest(unittest.TestCase):
    def test_shared_role_mutation_cannot_pass_preparation(self):
        with self.assertRaisesRegex(ValueError, 'instance role mutation'):
            scoped_sql.adapt('unknown.sql', 'ALTER ROLE ani_app NOLOGIN;')

    def test_unreviewed_transaction_is_rejected(self):
        with self.assertRaisesRegex(ValueError, 'transaction'):
            scoped_sql.adapt('unknown.sql', 'BEGIN;\nCREATE TABLE example(id int);\nCOMMIT;')

    def test_known_wrapper_keeps_body_with_scoped_grants(self):
        result = scoped_sql.adapt('20260821_001_tenant_admin_invitation.sql',
                                 'BEGIN;\nGRANT SELECT ON tenant_admin_invitation TO ani_app_user;\nCOMMIT;')
        self.assertNotIn('BEGIN;', result)
        self.assertIn('TO ani_fast_app_user', result)


if __name__ == '__main__':
    unittest.main()
