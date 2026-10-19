FROM python:3.12-slim
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
WORKDIR /app
COPY ml/ /app/ml/
RUN pip install --no-cache-dir '/app/ml[tracking]' && pip check
RUN useradd --uid 50000 --create-home ml && mkdir /artifacts && chown ml /artifacts
USER ml
ENTRYPOINT ["python", "-m", "matchlab_ml"]
