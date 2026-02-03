FROM python:3.14-slim

# Prevent Python from buffering stdout/stderr
ENV PYTHONUNBUFFERED=1

# Create non-root user (good practice)
RUN useradd -m botuser
WORKDIR /app

# Install dependencies
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy bot code
COPY bot.py .

# Drop privileges
USER botuser

# Run the bot
CMD ["python", "bot.py"]
