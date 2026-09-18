# syntax=docker/dockerfile:1.7
# Example FastAPI crawler node. Build with the repository root as context (the SDK is installed from
# sdk/python):
#
#   docker build -f examples/fastapi-crawler/Dockerfile -t spinneret-example-crawler:local .

ARG PYTHON_VERSION=3.12

FROM python:${PYTHON_VERSION}-slim AS build
ENV PIP_DISABLE_PIP_VERSION_CHECK=1 PIP_NO_CACHE_DIR=1
RUN python -m venv /opt/venv
ENV PATH=/opt/venv/bin:$PATH
COPY examples/fastapi-crawler/requirements.txt /tmp/requirements.txt
RUN pip install -r /tmp/requirements.txt
COPY sdk/python /tmp/sdk
RUN pip install /tmp/sdk

FROM python:${PYTHON_VERSION}-slim
RUN useradd --system --uid 10001 --home-dir /app --shell /usr/sbin/nologin crawler
COPY --from=build /opt/venv /opt/venv
WORKDIR /app
COPY examples/fastapi-crawler/app ./app
ENV PATH=/opt/venv/bin:$PATH \
    PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    SPINNERET_CACHE_DIR=/tmp/spinneret-cache
USER crawler
EXPOSE 8000
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=3 \
  CMD ["python", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8000/healthz', timeout=2)"]
CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8000", "--no-access-log", "--timeout-graceful-shutdown", "10"]
