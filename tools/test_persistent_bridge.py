import concurrent.futures
import json
import sys
import tempfile
import time
import unittest
from pathlib import Path
from tools.persistent_bridge import PersistentBridge,HelperProtocolError
from tools.repeat_helper300 import jobs
from tools import repeat_helper300 as repeated

FIXTURE = '''import json,os,sys,time
sys.stdin.reconfigure(encoding='utf-8');sys.stdout.reconfigure(encoding='utf-8')
mode=sys.argv[1]
for line in sys.stdin:
    envelope=json.loads(line); request=envelope['request']; id=envelope['request_id']
    if request['Action']=='ping': result=dict(ready=True,protocol='briefing-validation-v1')
    else:
        if mode=='timeout': time.sleep(1)
        if mode=='eof': sys.exit(0)
        if mode=='wrong': id='wrong-id'
        result=dict(error=None,validation_ms=0.1,echo=request['Input']['value'])
    result.update(request_id=id,process_id=os.getpid())
    print(json.dumps(dict(request_id=id,process_id=os.getpid(),result=result,protocol_error=None)),flush=True)
    if mode=='blocked' and request['Action']=='ping': time.sleep(3)
'''


class PersistentTests(unittest.TestCase):
    def helper(self,root,mode='ok'):
        script=root/'fixture.py';script.write_text(FIXTURE,encoding='utf8')
        # Windows venv python.exe is a redirector; use the real interpreter PID in this fixture.
        stats={};helper=PersistentBridge([getattr(sys,'_base_executable',sys.executable),'-u',str(script),mode],stats,'batch',timeout=3)
        self.assertEqual(stats['helper_spawns'],1)
        return helper

    def test_real_subprocess_ids_concurrency_unicode_and_duplicate(self):
        with tempfile.TemporaryDirectory() as name:
            helper=self.helper(Path(name))
            try:
                def request(i):
                    stats={};value=f'данные {i}: $(no-shell); `quotes`'
                    result=helper.request(dict(Action='validate',RequestID=f'job-{i}',Input=dict(value=value)),stats)
                    self.assertEqual(result['request_id'],f'job-{i}')
                    self.assertEqual(result['process_id'],helper.pid)
                    self.assertEqual(result['echo'],value)
                    self.assertEqual(stats['helper_requests'],1)
                    return result
                with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
                    self.assertEqual(len(list(pool.map(request,range(40)))),40)
                with self.assertRaisesRegex(HelperProtocolError,'duplicate_request_id'):request(0)
            finally:helper.close()
            self.assertEqual(helper.process.returncode,0)

    def test_timeout_eof_and_wrong_id_never_restart(self):
        for mode in ('timeout','eof','wrong'):
            with self.subTest(mode=mode),tempfile.TemporaryDirectory() as name:
                helper=self.helper(Path(name),mode);pid=helper.pid;helper.timeout=.1
                with self.assertRaises(HelperProtocolError):helper.request(dict(Action='validate',RequestID='one',Input=dict(value='private')), {})
                with self.assertRaises(HelperProtocolError):helper.request(dict(Action='validate',RequestID='two',Input=dict(value='private')), {})
                self.assertEqual(helper.pid,pid)
                with self.assertRaises(HelperProtocolError):helper.close()

    def test_repeat_job_ids_are_unique_but_user_json_unchanged(self):
        source=dict(cases=[dict(case_id=f'long-{i:02d}',user=json.dumps(dict(text=f'original {i}')),input=dict(messages=[]),user_sha256=str(i)) for i in range(1,31)])
        both=[jobs(source,'batch',mode) for mode in ('per_job_helper','persistent_helper')]
        self.assertEqual(len({r['job_id'] for group in both for r in group}),600)
        for group in both:
            self.assertEqual(len(group),300)
            self.assertEqual(len({r['user'] for r in group}),30)
            for i,job in enumerate(group):
                self.assertEqual(job['user'],source['cases'][i%30]['user'])
                self.assertEqual(job['repeat_index'],i//30+1)
                self.assertEqual(job['job_index'],i+1)
                self.assertNotIn(job['job_id'],job['user'])

    def test_deadline_also_covers_blocked_stdin(self):
        with tempfile.TemporaryDirectory() as name:
            helper=self.helper(Path(name),'blocked');helper.timeout=.1
            started=time.monotonic()
            with self.assertRaisesRegex(HelperProtocolError,'timeout'):
                helper.request(dict(Action='validate',RequestID='large',Input=dict(value='x'*1024*1024)),{})
            self.assertLess(time.monotonic()-started,3)
            with self.assertRaises(HelperProtocolError):helper.close()

    def test_refused_repeat_preserves_existing_report(self):
        import argparse
        with tempfile.TemporaryDirectory() as name:
            root=Path(name);(root/'per_job_helper').mkdir()
            (root/'benchmark.md').write_text('PRESERVE')
            cases=[dict(case_id=f'long-{i:02d}',user_sha256=str(i),input_file_sha256=str(i)) for i in range(1,31)]
            contract=dict(cases=cases,prompt_sha256='p',schema_sha256='s')
            plan=dict(repeat_helper300=dict(per_job_helper=300,persistent_helper=300,concurrency=2,synthetic_repeat=True,repetitions=10,job_ids_outside_model_input=True),manifest_sha256='m',prompt_sha256='p',schema_sha256='s',cases=[dict(c,budget_ok=True) for c in cases])
            (root/'plan.json').write_text(json.dumps(plan))
            args=argparse.Namespace(out_dir=root,manifest_hash='m',contract_telemetry={})
            with self.assertRaisesRegex(ValueError,'outputs_exist'):
                repeated.run_comparison(None,args,{},contract,root/'not-read')
            self.assertEqual((root/'benchmark.md').read_text(),'PRESERVE')
            self.assertFalse((root/'.generation.lock').exists())


if __name__=='__main__':unittest.main()
