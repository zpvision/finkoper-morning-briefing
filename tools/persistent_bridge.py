"""Bounded JSONL IPC to one diagnostic Go process; no shell, respawn or fallback."""
import concurrent.futures
import json
import queue
import subprocess
import threading
import time


class HelperProtocolError(ValueError):
    fatal_diagnostic = True


class PersistentBridge:
    def __init__(self, command, telemetry, prefix, timeout=30):
        self.timeout=timeout;self.lock=threading.Lock();self.outgoing=queue.Queue(maxsize=4)
        self.pending={};self.seen=set();self.failed=None;self.closing=False
        start=time.monotonic()
        self.process=subprocess.Popen(command,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL)
        telemetry["helper_spawn_ms"]=(time.monotonic()-start)*1000
        telemetry["helper_spawns"]=1
        self.pid=self.process.pid
        self.reader=threading.Thread(target=self._read,daemon=True);self.reader.start()
        self.writer=threading.Thread(target=self._write,daemon=True);self.writer.start()
        try:
            started=time.monotonic()
            response=self.request(dict(Action="ping",RequestID=prefix+":ready"),{})
            telemetry["helper_handshake_ms"]=(time.monotonic()-started)*1000
            telemetry["helper_control_requests"]=1
            if response.get("ready") is not True or response.get("protocol")!="briefing-validation-v1":
                raise HelperProtocolError("persistent_helper_handshake_failed")
        except BaseException:
            self._fail("persistent_helper_handshake_failed")
            self.close()
            raise

    def _fail(self,code):
        with self.lock:
            if self.failed:return
            self.failed=code;waiting=list(self.pending.values());self.pending.clear()
        for future in waiting:
            if not future.done():future.set_exception(HelperProtocolError(code))
        if self.process.poll() is None:self.process.kill()

    def _read(self):
        try:
            while True:
                raw=self.process.stdout.readline(8*1024*1024+4097)
                if not raw:
                    with self.lock:normal=self.closing and not self.pending
                    if not normal:self._fail("persistent_helper_eof")
                    return
                if len(raw)>8*1024*1024+4096 or not raw.endswith(b"\n"):
                    self._fail("persistent_helper_invalid_frame");return
                response=json.loads(raw)
                if not isinstance(response,dict) or response.get("process_id")!=self.pid:
                    self._fail("persistent_helper_pid_mismatch");return
                id=response.get("request_id")
                with self.lock:future=self.pending.pop(id,None)
                if future is None:
                    self._fail("persistent_helper_id_mismatch");return
                result=response.get("result")
                if response.get("protocol_error") or not isinstance(result,dict) or result.get("request_id")!=id or result.get("process_id")!=self.pid:
                    future.set_exception(HelperProtocolError("persistent_helper_invalid_response"))
                    self._fail("persistent_helper_invalid_response");return
                future.set_result(result)
        except Exception:
            self._fail("persistent_helper_invalid_response")

    def _write(self):
        while True:
            item=self.outgoing.get()
            if item is None:return
            raw,start,telemetry,written=item
            try:
                telemetry["helper_queue_wait_ms"]=(time.monotonic()-start)*1000
                begin=time.monotonic()
                self.process.stdin.write(raw);self.process.stdin.flush()
                telemetry["helper_write_ms"]=(time.monotonic()-begin)*1000
                written[0]=time.monotonic()
            except (OSError,ValueError):
                self._fail("persistent_helper_transport_failed");return

    def request(self, request, telemetry):
        id=request.get("RequestID")
        if not isinstance(id,str) or not id or len(id)>128:raise HelperProtocolError("persistent_helper_invalid_request_id")
        raw=json.dumps(dict(request_id=id,request=request),ensure_ascii=False).encode()+b"\n"
        if len(raw)>8*1024*1024+4096:raise HelperProtocolError("persistent_helper_request_too_large")
        future=concurrent.futures.Future()
        with self.lock:
            if self.failed or self.closing:raise HelperProtocolError(self.failed or "persistent_helper_closed")
            if id in self.seen:raise HelperProtocolError("persistent_helper_duplicate_request_id")
            if len(self.seen)>=100000:raise HelperProtocolError("persistent_helper_request_limit")
            self.seen.add(id);self.pending[id]=future
        start=time.monotonic()
        try:
            written=[None]
            self.outgoing.put_nowait((raw,start,telemetry,written))
            result=future.result(timeout=self.timeout)
            telemetry["helper_work_wait_ms"]=(time.monotonic()-(written[0] or start))*1000
            telemetry["helper_requests"]=1
            if "validation_ms" in result:telemetry["go_validation_ms"]=result["validation_ms"]
            return result
        except concurrent.futures.TimeoutError:
            self._fail("persistent_helper_timeout")
            raise HelperProtocolError("persistent_helper_timeout") from None
        except queue.Full:
            self._fail("persistent_helper_transport_failed")
            raise HelperProtocolError("persistent_helper_transport_failed") from None

    def close(self):
        with self.lock:self.closing=True
        try:self.outgoing.put_nowait(None)
        except queue.Full:self._fail("persistent_helper_shutdown_failed")
        self.writer.join(timeout=5)
        if self.writer.is_alive():self._fail("persistent_helper_shutdown_timeout")
        try:
            self.process.stdin.close()
        except (OSError,ValueError):pass
        try:self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self._fail("persistent_helper_shutdown_timeout");self.process.wait(timeout=5)
        self.reader.join(timeout=5)
        self.process.stdout.close()
        if self.failed:raise HelperProtocolError(self.failed)
        if self.process.returncode!=0:raise HelperProtocolError("persistent_helper_exit_failed")
