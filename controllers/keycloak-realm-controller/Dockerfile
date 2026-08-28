FROM python:3.12-slim

WORKDIR /app

COPY requirements.txt .

RUN pip install --no-cache-dir -r requirements.txt

COPY main.py .

# Run as non-root
RUN useradd --create-home --uid 10001 controller
USER 10001

CMD ["python", "main.py"]