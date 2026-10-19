"""Create an Airflow-owned private password file, then replace this process.

A host-owned 0600 bind mount is not readable by container UID 50000 on all hosts.
Keep the generated host copy private for the operator, but provision this copy
under the Airflow container's own UID. Do not leave the password in task env.
"""

import json
import os


def main():
    if os.environ.get("ML_WAIT_DATABASE") == "true":
        from matchlab_ml.provision import wait_database
        wait_database(os.environ["AIRFLOW__DATABASE__SQL_ALCHEMY_CONN"].replace("postgresql+psycopg2://", "postgresql://"))
    password = os.environ.pop("ML_AIRFLOW_ADMIN_PASSWORD")
    path = os.environ["AIRFLOW__CORE__SIMPLE_AUTH_MANAGER_PASSWORDS_FILE"]
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, "w") as stream:
        os.fchmod(stream.fileno(), 0o600)
        json.dump({"analyst": password}, stream)
    os.execvp("airflow", ["airflow", "standalone"])


if __name__ == "__main__":
    main()
