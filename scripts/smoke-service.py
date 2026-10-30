#!/usr/bin/env python3
"""Exercise the packaged service, using only mock devices and temporary data."""
import http.cookiejar
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.request
import uuid

engine = os.environ.get('CONTAINER_ENGINE', 'docker')
image = sys.argv[1] if len(sys.argv) > 1 else 'nvidia-fan-control:local'
name = 'fan-control-smoke-' + uuid.uuid4().hex[:12]
token = 'container-test-only-' + uuid.uuid4().hex


def command(*args):
    return subprocess.check_output([engine, *args], text=True).strip()


with tempfile.TemporaryDirectory(prefix='fan-control-smoke-') as temporary:
    root = Path(temporary)
    data = root / 'data'
    data.mkdir()
    secret = root / 'admin-token'
    secret.write_text(token)
    secret.chmod(0o600)

    def launch():
        command('run', '-d', '--name', name, '--read-only',
                '-p', '127.0.0.1::8080', '-v', f'{data}:/data:Z',
                '-v', f'{secret}:/run/secrets/admin-token:ro,Z', image,
                '--mock', '--listen', '0.0.0.0:8080', '--config', '/data/config.json',
                '--admin-token-file', '/run/secrets/admin-token')
        port = command('port', name, '8080/tcp').rsplit(':', 1)[1]
        base = f'http://127.0.0.1:{port}'
        opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        for _ in range(100):
            try:
                opener.open(base + '/healthz', timeout=1).close()
                break
            except (OSError, urllib.error.URLError):
                time.sleep(0.1)
        else:
            raise AssertionError('container HTTP startup failed')
        html = opener.open(base + '/').read().decode()
        assert '<script' in html and 'assets/' in html, 'embedded UI missing'
        try:
            opener.open(base + '/api/v1/config')
            raise AssertionError('configuration was accessible without authentication')
        except urllib.error.HTTPError as error:
            assert error.code == 401
        csrf = ''

        def request(path, method='GET', body=None, revision=None):
            headers = {'Origin': base, 'X-CSRF-Token': csrf}
            if body is not None:
                headers['Content-Type'] = 'application/json'
            if revision is not None:
                headers['If-Match'] = f'"{revision}"'
            encoded = None if body is None else json.dumps(body).encode()
            req = urllib.request.Request(base + '/api/v1' + path, data=encoded, method=method, headers=headers)
            with opener.open(req, timeout=15) as response:
                return json.load(response)

        csrf = request('/session', 'POST', {'token': token})['csrf_token']
        assert request('/status')['mock'] is True
        for _ in range(100):
            if request('/status')['available'] and len(request('/gpus')['gpus']) == 2:
                break
            time.sleep(0.1)
        else:
            raise AssertionError('mock telemetry did not become available')
        return request

    def stop_and_remove():
        command('stop', '--time', '25', name)
        assert command('inspect', '--format', '{{.State.ExitCode}}', name) == '0', 'shutdown failed'
        # Rootful Docker writes root-owned 0600 files in the bind mount. Read
        # the stopped container through the engine instead of the host user;
        # consume the archive in memory without extracting files or permissions.
        archive = subprocess.check_output([
            engine, 'cp', f'{name}:/data/ownership.mock.json', '-',
        ])
        with tarfile.open(fileobj=io.BytesIO(archive)) as copied:
            member = copied.getmember('ownership.mock.json')
            assert member.isfile(), 'ownership journal is not a regular file'
            with copied.extractfile(member) as journal_file:
                journal = json.load(journal_file)
        assert journal['fans'] == {}, 'SIGTERM did not clear restored ownership'
        command('rm', name)

    try:
        request = launch()
        cfg = request('/config')
        cfg['gpus']['GPU-MOCK-001'] = {
            'mode': 'curve', 'curve': 'Balanced', 'fans': [], 'min_duty': 20,
            'critical_temp': 90, 'hysteresis': 3, 'cooling_hold_seconds': 10,
            'limits_confirmed': True,
        }
        saved = request('/config', 'PUT', cfg, cfg['revision'])
        gpu = request('/gpus')['gpus'][0]
        assert gpu['state'] == 'curve' and len(gpu['fans']) == 2
        assert all(fan['commanded_duty'] is not None for fan in gpu['fans'])
        assert gpu['applied_revision'] == saved['revision']
        stop_and_remove()

        request = launch()
        assert request('/config')['revision'] == saved['revision'], 'configuration did not survive recreation'
        assert request('/gpus')['gpus'][0]['state'] == 'curve'
        result = request('/gpus/GPU-MOCK-001/default', 'POST', {})
        assert result['restored'] and result['persisted']
        assert request('/gpus')['gpus'][0]['state'] == 'automatic'
        stop_and_remove()

        request = launch()
        assert request('/config')['gpus']['GPU-MOCK-001']['mode'] == 'automatic'
        assert request('/gpus')['gpus'][0]['state'] == 'automatic'
        stop_and_remove()
        print('Container mock smoke passed: embedded UI, authentication, both fans, SIGTERM restoration, recreation and persistent automatic mode.')
    finally:
        subprocess.run([engine, 'rm', '-f', name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
