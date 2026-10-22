"""Exercise non-root images with disposable containers and an isolated MLflow volume."""
import json
import subprocess
import sys
import time
import urllib.request
import uuid


def docker(*args):
    return subprocess.check_output(["docker", *args], text=True).strip()


def python(image, code):
    return docker("run", "--rm", "--entrypoint", "python", image, "-c", code)


def endpoint(container, port):
    info = json.loads(docker("inspect", container))[0]
    binding = info["NetworkSettings"]["Ports"][f"{port}/tcp"][0]
    return f"http://127.0.0.1:{binding['HostPort']}"


def wait_http(url):
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=3) as response:
                if response.status == 200:
                    return
        except (OSError, TimeoutError):
            pass
        time.sleep(1)
    raise RuntimeError(f"Container did not become ready: {url}")


def main(service, image):
    identity = docker("image", "inspect", image, "--format", "{{.Config.User}}")
    assert identity and identity.split(":", 1)[0] not in ("root", "0"), identity
    if service == "airflow":
        print(python(image, """
import os
from pathlib import Path
import airflow
import matchlab_ml.pipeline
assert os.getuid() != 0
home = Path(os.environ.get('AIRFLOW_HOME', '/opt/airflow'))
(home / 'runtime-smoke').write_text('non-root')
assert Path('/opt/airflow/dags/matchlab_recommendations.py').is_file()
assert Path('/opt/airflow/bootstrap/start_airflow.py').is_file()
print('Airflow imports and writable home verified, UID', os.getuid())
"""))
        return
    prefix = "matchlab-security-" + uuid.uuid4().hex[:12]
    containers = []
    volume = None
    try:
        if service == "api-docs":
            name = prefix + "-docs"
            containers.append(name)
            docker("run", "-d", "--name", name, "-p", "127.0.0.1::8080",
                   "-e", "GATEWAY_URL=http://127.0.0.1:8081", image)
            url = endpoint(name, 8080)
            for path in ("/docs/", "/docs/openapi.yaml", "/docs/scalar.js"):
                wait_http(url + path)
            assert docker("exec", name, "id", "-u") == "101"
            print("Scalar/nginx serves documentation as UID 101")
        elif service == "mlflow":
            volume = docker("volume", "create", prefix + "-data")
            command = ("mlflow db upgrade sqlite:////mlflow/mlflow.db && "
                       "exec mlflow server --host 0.0.0.0 --port 5000 --workers 1 "
                       "--backend-store-uri sqlite:////mlflow/mlflow.db "
                       "--artifacts-destination /mlflow/artifacts --serve-artifacts "
                       "--allowed-hosts localhost:5000,127.0.0.1:5000")
            def start(suffix):
                name = prefix + suffix
                containers.append(name)
                docker("run", "-d", "--name", name, "-p", "127.0.0.1::5000",
                       "-v", volume + ":/mlflow", "--entrypoint", "sh", image, "-ec", command)
                wait_http(endpoint(name, 5000) + "/health")
                return name
            first = start("-first")
            run_id = docker("exec", first, "python", "-c", """
import os
from pathlib import Path
from mlflow import MlflowClient
assert os.getuid() == 50000
client = MlflowClient(tracking_uri='http://127.0.0.1:5000')
experiment = client.create_experiment('non-root-runtime')
run = client.create_run(experiment)
client.log_metric(run.info.run_id, 'smoke', 1.0)
Path('/tmp/evidence.txt').write_text('persisted-non-root')
client.log_artifact(run.info.run_id, '/tmp/evidence.txt')
client.set_terminated(run.info.run_id)
print(run.info.run_id)
""").splitlines()[-1]
            docker("rm", "-f", first)
            second = start("-second")
            print(docker("exec", second, "python", "-c", """
import sys
from pathlib import Path
from mlflow import MlflowClient
client = MlflowClient(tracking_uri='http://127.0.0.1:5000')
run = client.get_run(sys.argv[1])
assert run.data.metrics['smoke'] == 1.0
path = client.download_artifacts(sys.argv[1], 'evidence.txt', '/tmp')
assert Path(path).read_text() == 'persisted-non-root'
print('MLflow metric and artifact survived container recreation as UID 50000')
""", run_id))
        else:
            raise ValueError(service)
    except Exception:
        for name in containers:
            subprocess.run(["docker", "logs", "--tail", "80", name], check=False)
        raise
    finally:
        for name in containers:
            subprocess.run(["docker", "rm", "-f", name], check=False, stdout=subprocess.DEVNULL,
                           stderr=subprocess.DEVNULL)
        if volume:
            subprocess.run(["docker", "volume", "rm", volume], check=False)


if __name__ == "__main__":
    main(*sys.argv[1:])
