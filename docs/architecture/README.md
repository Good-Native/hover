# Architecture Documentation

System design, database schema, and API specifications for Hover.

## 📄 Documents

### Core Architecture

- **[ARCHITECTURE.md](./ARCHITECTURE.md)** - System design, components, worker
  pools, and job lifecycle
- **[CRAWL_HANDLING.md](./CRAWL_HANDLING.md)** - Case → action table: what we do
  to a job and its tasks for every domain or page condition (WAF walls,
  robots.txt outcomes, response codes, timeouts, etc.). **Add a row when you add
  a case.**
- **[CONFIG_REFERENCE.md](./CONFIG_REFERENCE.md)** - Every configurable dial:
  env vars, hardcoded constants, and their relationships
- **[DATABASE.md](./DATABASE.md)** - PostgreSQL schema, queries, and performance
  optimisation
- **[API.md](./API.md)** - RESTful API endpoints, authentication, and response
  formats
- **[webflow-designer-extension.md](./webflow-designer-extension.md)** - Webflow
  Designer Extension integration architecture

## 🏗️ System Overview

Hover uses a PostgreSQL-backed worker pool architecture for efficient URL
crawling and cache warming.

### Key Components

- **Worker Pool** - Concurrent job processing with configurable limits
- **Job Queue** - PostgreSQL-based task queue with row-level locking
- **API Layer** - RESTful endpoints with JWT authentication
- **Crawler** - Intelligent sitemap processing and link discovery

## 🔗 Related Documentation

- [Development Setup](../development/DEVELOPMENT.md) - Get the system running
- [Testing Strategy](../testing/) - How to test the architecture
- [Database Operations](./DATABASE.md) - Schema and query details
