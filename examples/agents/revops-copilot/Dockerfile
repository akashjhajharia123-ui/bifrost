FROM python:3.12-slim
COPY --from=ghcr.io/astral-sh/uv:0.11.0 /uv /usr/local/bin/uv
WORKDIR /app
COPY pyproject.toml uv.lock README.md ./
COPY src ./src
COPY scenarios ./scenarios
RUN uv sync --frozen --no-dev
ENV PATH="/app/.venv/bin:$PATH"
