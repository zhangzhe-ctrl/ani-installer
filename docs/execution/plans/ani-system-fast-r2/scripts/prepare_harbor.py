#!/usr/bin/env python3
"""Create only this task's private Harbor project and scoped pull robot.

Run on Fedora inside the existing cluster lock. Credentials and returned robot
secret remain in the supplied private access directory; no shared policy edits.
"""
import argparse
import base64
import json
import os
from pathlib import Path
import subprocess

HOST = '172.16.101.10:30003'
PROJECT = 'ani-system-fast-20260930'


def prepare(access):
    auth = json.loads((access / 'harbor-admin-auth.json').read_text())['auths'][HOST]['auth']
    def request(method, path, body=None):
        config = 'header = "Authorization: Basic ' + auth + '"\n'
        command = ['curl', '--silent', '--show-error', '--fail-with-body', '--connect-timeout', '10',
                   '--max-time', '60', '--cacert', str(access / 'registry-ca/ca.crt'), '--config', '-',
                   '-X', method, 'https://' + HOST + '/api/v2.0/' + path]
        if body is not None:
            # The request body contains permissions only; no passwords.
            command += ['-H', 'Content-Type: application/json', '--data', json.dumps(body)]
        result = subprocess.run(command, input=config.encode(), capture_output=True)
        if result.returncode:
            raise RuntimeError('Harbor API failed: ' + method + ' ' + path + ' rc=' + str(result.returncode))
        return json.loads(result.stdout) if result.stdout.strip() else None
    marker = access / 'harbor-task-project.json'
    projects = request('GET', 'projects?name=' + PROJECT)
    projects = [p for p in projects if p['name'] == PROJECT]
    if projects and not marker.exists():
        raise RuntimeError('existing Harbor project has no task creation record; no takeover')
    if not projects:
        request('POST', 'projects', {'project_name': PROJECT, 'public': False})
        projects = request('GET', 'projects?name=' + PROJECT)
        marker.write_text(json.dumps(projects[0], indent=2) + '\n')
        marker.chmod(0o600)
    project = projects[0]
    if project['metadata'].get('public') != 'false':
        raise RuntimeError('task project is unexpectedly public')
    robot_path = access / 'harbor-task-pull-robot.json'
    if not robot_path.exists():
        robot = request('POST', 'robots', {'name': 'ani-fast-pull', 'description': PROJECT,
            'level': 'project', 'duration': 90,
            'permissions': [{'kind': 'project', 'namespace': PROJECT,
                             'access': [{'resource': 'repository', 'action': 'pull'},
                                        {'resource': 'repository', 'action': 'list'},
                                        {'resource': 'artifact', 'action': 'read'},
                                        {'resource': 'artifact', 'action': 'list'}]}]})
        robot_path.write_text(json.dumps(robot, indent=2) + '\n'); robot_path.chmod(0o600)
    robot = json.loads(robot_path.read_text())
    value = base64.b64encode((robot['name'] + ':' + robot['secret']).encode()).decode()
    pull_auth = access / 'harbor-task-pull-auth.json'
    pull_auth.write_text(json.dumps({'auths': {HOST: {'auth': value}}}) + '\n'); pull_auth.chmod(0o600)
    print('Task private Harbor project and scoped pull robot ready; project_id=' + str(project['project_id']))


if __name__ == '__main__':
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--access', type=Path, required=True)
    prepare(parser.parse_args().access)
