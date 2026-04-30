# API Reference

## Overview

This document defines the comprehensive API design for Hover's multi-interface
architecture. The API follows RESTful principles with consistent response
formats to support web applications, Slack integrations, Webflow extensions, and
future interfaces.

## Current Status

✅ **Core API Infrastructure Implemented:**

- Standardised error handling with request IDs and proper HTTP status codes
- RESTful API structure with consistent response formats
- Comprehensive middleware stack (CORS, request ID, logging, rate limiting)
- Authentication integration with Supabase JWT validation

✅ **Current Endpoints:**

- `/health` - Service health check
- `/health/db` - PostgreSQL health check
- `/v1/jobs` - RESTful job management (GET/POST)
- `/v1/jobs/:id` - Individual job operations (GET/PUT/DELETE)
- `/v1/schedulers` - Recurring job scheduler management (GET/POST/PUT/DELETE)
- `/v1/auth/register` - User registration
- `/v1/auth/profile` - User profile (authenticated)
- `/v1/auth/session` - Session validation
- `/admin/reset-db` - Admin database reset (system administrators only)

🔄 **Next Implementation Phase:**

- Complete CRUD operations for jobs (cancel, retry)
- Task management endpoints (`/v1/jobs/:id/tasks`)
- API key management (`/v1/auth/api-keys`)
- Organisation management (`/v1/organisations`)
- Webhook system (`/v1/webhooks`)
- Export functionality (`/v1/jobs/:id/export`)

## API Structure

### Base URL

```
Local Development: http://localhost:8080 (Hover application)
Production Application: https://hover.app.goodnative.co (Live application, services, demo pages)
Marketing Site: https://goodnative.co (Marketing website only)
```

**Note**:

- For local development and testing, use `http://localhost:8080`
- For production application access, use `https://hover.app.goodnative.co`
- `https://goodnative.co` is only the marketing website

### Versioning

All API endpoints are versioned under `/v1/` to ensure backward compatibility.

## Authentication

### Methods Supported

1. **JWT Bearer Token** (Primary)

   ```
   Authorization: Bearer <jwt_token>
   ```

   - Used by web applications
   - Tokens issued by Supabase Auth
   - Short expiry with refresh capability

2. **API Key** (For Integrations)

   ```
   Authorization: Bearer <api_key>
   X-API-Key: <api_key>
   ```

   - Used by Slack, CLI tools, and integrations
   - Long-lived keys with scoped permissions
   - Managed through user dashboard

### Protected Resources

All endpoints under `/v1/` require authentication except:

- `/health`
- `/v1/auth/*` (registration, session validation)

### Future: Platform Authentication

- Planning to support Shopify and Webflow app authentication
- Will use organisation-based data isolation
- Each platform store/site maps to an organisation
- See `/plans/platform-auth-architecture.md` for details

## Standard Response Format

### Success Response

```json
{
  "status": "success",
  "data": {
    // Response data varies by endpoint
  },
  "meta": {
    "timestamp": "2023-05-18T12:34:56Z",
    "version": "1.0.0",
    "request_id": "req_123abc"
  }
}
```

### Error Response

```json
{
  "status": "error",
  "error": {
    "code": "invalid_request",
    "message": "Invalid job configuration",
    "details": {
      "field": "max_pages",
      "issue": "Must be a positive integer"
    }
  },
  "meta": {
    "timestamp": "2023-05-18T12:34:56Z",
    "version": "1.0.0",
    "request_id": "req_123abc"
  }
}
```

### HTTP Status Codes

- `200` - Success
- `201` - Created
- `400` - Bad Request
- `401` - Unauthorized
- `403` - Forbidden
- `404` - Not Found
- `409` - Conflict
- `422` - Unprocessable Entity
- `429` - Too Many Requests
- `500` - Internal Server Error

## Core Resources

### Jobs

#### Create Job

```http
POST /v1/jobs
Content-Type: application/json
Authorization: Bearer <token>

{
  "domain": "example.com",
  "options": {
    "use_sitemap": true,
    "find_links": true,
    "max_pages": 100,
    "concurrency": 20
  }
}
```

**Response (201):**

```json
{
  "status": "success",
  "data": {
    "id": "job_123abc",
    "domain": "example.com",
    "status": "created",
    "organisation_id": "org_456def",
    "options": {
      "use_sitemap": true,
      "find_links": true,
      "max_pages": 100,
      "concurrency": 20
    },
    "created_at": "2023-05-18T12:34:56Z"
  },
  "meta": {
    "timestamp": "2023-05-18T12:34:56Z",
    "version": "1.0.0"
  }
}
```

#### List Jobs

```http
GET /v1/jobs?page=1&limit=20&status=running
Authorization: Bearer <token>
```

**Response (200):**

```json
{
  "status": "success",
  "data": {
    "jobs": [
      {
        "id": "job_123abc",
        "domain": "example.com",
        "status": "running",
        "progress": {
          "total_tasks": 150,
          "completed_tasks": 45,
          "failed_tasks": 2,
          "skipped_tasks": 0,
          "percentage": 31.33
        },
        "created_at": "2023-05-18T12:34:56Z",
        "updated_at": "2023-05-18T12:45:12Z"
      }
    ],
    "pagination": {
      "page": 1,
      "limit": 20,
      "total": 1,
      "has_next": false
    }
  },
  "meta": {
    "timestamp": "2023-05-18T12:34:56Z",
    "version": "1.0.0"
  }
}
```

#### Get Job

```http
GET /v1/jobs/{job_id}
Authorization: Bearer <token>
```

**Response (200):**

```json
{
  "status": "success",
  "data": {
    "id": "job_123abc",
    "domain": "example.com",
    "status": "running",
    "organisation_id": "org_456def",
    "progress": {
      "total_tasks": 150,
      "completed_tasks": 45,
      "failed_tasks": 2,
      "skipped_tasks": 0,
      "percentage": 31.33
    },
    "stats": {
      "avg_response_time": 234,
      "cache_hit_ratio": 0.85,
      "total_bytes": 2048576
    },
    "options": {
      "use_sitemap": true,
      "find_links": true,
      "max_pages": 100,
      "concurrency": 20
    },
    "created_at": "2023-05-18T12:34:56Z",
    "updated_at": "2023-05-18T12:45:12Z",
    "started_at": "2023-05-18T12:35:01Z",
    "completed_at": null
  },
  "meta": {
    "timestamp": "2023-05-18T12:34:56Z",
    "version": "1.0.0"
  }
}
```

#### Cancel Job

```http
POST /v1/jobs/{job_id}/cancel
Authorization: Bearer <token>
```

**Response (200):**

```json
{
  "status": "success",
  "data": {
    "id": "job_123abc",
    "status": "cancelled",
    "cancelled_at": "2023-05-18T12:50:00Z"
  },
  "meta": {
    "timestamp": "2023-05-18T12:50:00Z",
    "version": "1.0.0"
  }
}
```

### Tasks

#### List Tasks for Job

```http
GET /v1/jobs/{job_id}/tasks?page=1&limit=50&status=failed&status_code=404&min_response_time=5000
Authorization: Bearer <token>
```

**Query Parameters:**

- `page` - Page number (default: 1)
- `limit` - Results per page (default: 50, max: 100)
- `status` - Filter by task status: `waiting`, `pending`, `running`,
  `completed`, `failed`, `skipped` (see [`CRAWL_HANDLING.md`](CRAWL_HANDLING.md)
  for what each means)
- `status_code` - Filter by HTTP status code: `200`, `404`, `500`, etc.
- `min_response_time` - Minimum response time in milliseconds
- `max_response_time` - Maximum response time in milliseconds
- `cache_status` - Filter by cache status: `hit`, `miss`, `error`
- `has_error` - Filter tasks with/without errors: `true`, `false`
- `sort` - Sort order: `created_at`, `response_time`, `status_code` (add `-` for
  desc)

**Pagination Strategy:**

- **Default**: 50 results per page (good balance of data vs performance)
- **Maximum**: 100 results per page (prevents overwhelming responses)
- **Large datasets**: Use filtering to reduce total results before pagination
- **Export option**: For bulk data access, use the export endpoint instead

**Response (200):**

```json
{
  "status": "success",
  "data": {
    "tasks": [
      {
        "id": "task_789xyz",
        "job_id": "job_123abc",
        "url": "https://example.com/page1",
        "status": "failed",
        "status_code": 404,
        "response_time": null,
        "cache_status": "miss",
        "content_length": 0,
        "content_type": null,
        "error_message": "Page not found",
        "attempts": 3,
        "discovered_from": "sitemap",
        "redirect_url": null,
        "created_at": "2023-05-18T12:35:01Z",
        "updated_at": "2023-05-18T12:40:15Z",
        "completed_at": "2023-05-18T12:40:15Z"
      }
    ],
    "pagination": {
      "page": 1,
      "limit": 50,
      "total": 2,
      "total_pages": 1,
      "has_next": false,
      "has_prev": false,
      "next_page": null,
      "prev_page": null
    },
    "summary": {
      "total_tasks": 150,
      "by_status": {
        "completed": 145,
        "failed": 5,
        "pending": 0,
        "running": 0
      },
      "by_status_code": {
        "200": 145,
        "404": 3,
        "500": 2
      },
      "performance": {
        "avg_response_time": 234,
        "median_response_time": 198,
        "slow_pages_count": 12,
        "fast_pages_count": 133
      }
    }
  },
  "meta": {
    "timestamp": "2023-05-18T12:34:56Z",
    "version": "1.0.0"
  }
}
```

#### Get Task Results Summary

```http
GET /v1/jobs/{job_id}/results
Authorization: Bearer <token>
```

**Response (200):**

```json
{
  "status": "success",
  "data": {
    "job_id": "job_123abc",
    "summary": {
      "total_pages": 150,
      "successful_pages": 145,
      "failed_pages": 5,
      "avg_response_time": 234,
      "total_bytes_transferred": 15728640
    },
    "issues": {
      "not_found_pages": [
        {
          "url": "https://example.com/missing-page",
          "status_code": 404,
          "discovered_from": "link_crawl"
        }
      ],
      "slow_pages": [
        {
          "url": "https://example.com/slow-page",
          "response_time": 8500,
          "status_code": 200
        }
      ],
      "server_errors": [
        {
          "url": "https://example.com/error-page",
          "status_code": 500,
          "error_message": "Internal server error"
        }
      ],
      "redirects": [
        {
          "url": "https://example.com/old-page",
          "redirect_url": "https://example.com/new-page",
          "status_code": 301
        }
      ]
    },
    "performance_breakdown": {
      "under_1s": 120,
      "1s_to_3s": 25,
      "3s_to_5s": 3,
      "over_5s": 2
    },
    "cache_analysis": {
      "cache_hits": 120,
      "cache_misses": 25,
      "cache_errors": 5,
      "hit_ratio": 0.8
    }
  },
  "meta": {
    "timestamp": "2023-05-18T12:34:56Z",
    "version": "1.0.0"
  }
}
```

#### Export Task Results

```http
GET /v1/jobs/{job_id}/export?format=csv&include=url,status_code,response_time,cache_status
Authorization: Bearer <token>
```

**Query Parameters:**

- `format` - Export format: `csv`, `json`, `xlsx`
- `include` - Fields to include (comma-separated)
- `filter` - Same filter options as task listing

**Response (200):**

```
Content-Type: text/csv
Content-Disposition: attachment; filename="job_123abc_results.csv"

url,status_code,response_time,cache_status,error_message
https://example.com/page1,200,234,hit,
https://example.com/page2,404,0,miss,Page not found
https://example.com/page3,500,1200,error,Internal server error
```

#### Retry Failed Tasks

```http
POST /v1/jobs/{job_id}/tasks/retry
Authorization: Bearer <token>

{
  "task_ids": ["task_789xyz", "task_101abc"]
}
```

### Schedulers (Recurring Jobs)

Schedulers enable automatic recurring job execution at specified intervals (6,
12, 24, or 48 hours).

#### Create Scheduler

```http
POST /v1/schedulers
Authorization: Bearer <token>

{
  "domain": "example.com",
  "schedule_interval_hours": 24,
  "concurrency": 20,
  "find_links": true,
  "max_pages": 0,
  "include_paths": "/blog/*,/products/*",
  "exclude_paths": "/admin/*",
  "required_workers": 1
}
```

**Response (201):**

```json
{
  "status": "success",
  "data": {
    "id": "sched_abc123",
    "domain_id": 42,
    "organisation_id": "org_456def",
    "schedule_interval_hours": 24,
    "next_run_at": "2025-12-23T14:30:00Z",
    "is_enabled": true,
    "concurrency": 20,
    "find_links": true,
    "max_pages": 0,
    "include_paths": "/blog/*,/products/*",
    "exclude_paths": "/admin/*",
    "required_workers": 1,
    "created_at": "2025-12-22T14:30:00Z",
    "updated_at": "2025-12-22T14:30:00Z"
  },
  "meta": {
    "timestamp": "2025-12-22T14:30:00Z",
    "version": "1.0.0"
  }
}
```

#### List Schedulers

```http
GET /v1/schedulers
Authorization: Bearer <token>
```

**Response (200):**

```json
{
  "status": "success",
  "data": {
    "schedulers": [
      {
        "id": "sched_abc123",
        "domain_name": "example.com",
        "domain_id": 42,
        "organisation_id": "org_456def",
        "schedule_interval_hours": 24,
        "next_run_at": "2025-12-23T14:30:00Z",
        "is_enabled": true,
        "concurrency": 20,
        "find_links": true,
        "max_pages": 0,
        "created_at": "2025-12-22T14:30:00Z",
        "updated_at": "2025-12-22T14:30:00Z"
      }
    ]
  },
  "meta": {
    "timestamp": "2025-12-22T14:30:00Z",
    "version": "1.0.0"
  }
}
```

#### Get Scheduler

```http
GET /v1/schedulers/{scheduler_id}
Authorization: Bearer <token>
```

**Response (200):**

```json
{
  "status": "success",
  "data": {
    "id": "sched_abc123",
    "domain_name": "example.com",
    "domain_id": 42,
    "organisation_id": "org_456def",
    "schedule_interval_hours": 24,
    "next_run_at": "2025-12-23T14:30:00Z",
    "is_enabled": true,
    "concurrency": 20,
    "find_links": true,
    "max_pages": 0,
    "include_paths": "/blog/*,/products/*",
    "exclude_paths": "/admin/*",
    "required_workers": 1,
    "created_at": "2025-12-22T14:30:00Z",
    "updated_at": "2025-12-22T14:30:00Z"
  },
  "meta": {
    "timestamp": "2025-12-22T14:30:00Z",
    "version": "1.0.0"
  }
}
```

#### Update Scheduler

```http
PUT /v1/schedulers/{scheduler_id}
Authorization: Bearer <token>

{
  "schedule_interval_hours": 12,
  "is_enabled": false
}
```

**Notes:**

- All fields are optional; only provided fields will be updated
- Use `null` for optional fields like `include_paths` to clear them

**Response (200):**

```json
{
  "status": "success",
  "data": {
    "id": "sched_abc123",
    "domain_name": "example.com",
    "domain_id": 42,
    "organisation_id": "org_456def",
    "schedule_interval_hours": 12,
    "next_run_at": "2025-12-23T02:30:00Z",
    "is_enabled": false,
    "concurrency": 20,
    "find_links": true,
    "max_pages": 0,
    "created_at": "2025-12-22T14:30:00Z",
    "updated_at": "2025-12-22T14:35:00Z"
  },
  "meta": {
    "timestamp": "2025-12-22T14:35:00Z",
    "version": "1.0.0"
  }
}
```

#### Delete Scheduler

```http
DELETE /v1/schedulers/{scheduler_id}
Authorization: Bearer <token>
```

**Response (200):**

```json
{
  "status": "success",
  "data": {
    "message": "Scheduler deleted successfully"
  },
  "meta": {
    "timestamp": "2025-12-22T14:40:00Z",
    "version": "1.0.0"
  }
}
```

#### List Jobs for Scheduler

```http
GET /v1/schedulers/{scheduler_id}/jobs
Authorization: Bearer <token>
```

**Response (200):**

```json
{
  "status": "success",
  "data": {
    "jobs": [
      {
        "id": "job_123abc",
        "scheduler_id": "sched_abc123",
        "domain": "example.com",
        "status": "completed",
        "source_type": "scheduler",
        "created_at": "2025-12-22T14:30:00Z",
        "completed_at": "2025-12-22T14:45:00Z"
      }
    ]
  },
  "meta": {
    "timestamp": "2025-12-22T14:50:00Z",
    "version": "1.0.0"
  }
}
```

### Authentication & Users

#### Get Current User Profile

```http
GET /v1/auth/profile
Authorization: Bearer <token>
```

**Response (200):**

```json
{
  "status": "success",
  "data": {
    "user": {
      "id": "user_123",
      "email": "user@example.com",
      "full_name": "John Doe",
      "organisation_id": "org_456def",
      "created_at": "2023-05-18T10:00:00Z"
    },
    "organisation": {
      "id": "org_456def",
      "name": "Example Organisation",
      "created_at": "2023-05-18T10:00:00Z"
    }
  },
  "meta": {
    "timestamp": "2023-05-18T12:34:56Z",
    "version": "1.0.0"
  }
}
```

### API Keys

#### List API Keys

```http
GET /v1/auth/api-keys
Authorization: Bearer <token>
```

**Response (200):**

```json
{
  "status": "success",
  "data": {
    "api_keys": [
      {
        "id": "key_123abc",
        "name": "Slack Integration",
        "prefix": "sk_live_...",
        "scopes": ["jobs:read", "jobs:create"],
        "last_used": "2023-05-18T10:30:00Z",
        "created_at": "2023-05-15T14:00:00Z"
      }
    ]
  },
  "meta": {
    "timestamp": "2023-05-18T12:34:56Z",
    "version": "1.0.0"
  }
}
```

#### Create API Key

```http
POST /v1/auth/api-keys
Authorization: Bearer <token>

{
  "name": "Slack Integration",
  "scopes": ["jobs:read", "jobs:create"]
}
```

**Response (201):**

```json
{
  "status": "success",
  "data": {
    "id": "key_123abc",
    "name": "Slack Integration",
    "key": "sk_live_abcd1234...",
    "scopes": ["jobs:read", "jobs:create"],
    "created_at": "2023-05-18T12:34:56Z"
  },
  "meta": {
    "timestamp": "2023-05-18T12:34:56Z",
    "version": "1.0.0"
  }
}
```

#### Revoke API Key

```http
DELETE /v1/auth/api-keys/{key_id}
Authorization: Bearer <token>
```

### Organisations

#### Get Organisation Details

```http
GET /v1/organisations/current
Authorization: Bearer <token>
```

**Response (200):**

```json
{
  "status": "success",
  "data": {
    "id": "org_456def",
    "name": "Example Organisation",
    "members": [
      {
        "id": "user_123",
        "email": "user@example.com",
        "full_name": "John Doe",
        "role": "owner",
        "joined_at": "2023-05-18T10:00:00Z"
      }
    ],
    "usage": {
      "jobs_this_month": 15,
      "pages_crawled_this_month": 2500
    },
    "created_at": "2023-05-18T10:00:00Z"
  },
  "meta": {
    "timestamp": "2023-05-18T12:34:56Z",
    "version": "1.0.0"
  }
}
```

### System Endpoints

#### Health Check

```http
GET /health
```

**Response (200):**

```json
{
  "status": "success",
  "data": {
    "service": "healthy",
    "database": "healthy",
    "version": "1.0.0",
    "uptime": "72h15m30s"
  },
  "meta": {
    "timestamp": "2023-05-18T12:34:56Z",
    "version": "1.0.0"
  }
}
```

### System Administrator Endpoints

These endpoints require system administrator privileges. See
[SECURITY.md](../SECURITY.md#system-administrator-role) for setup instructions.

#### Database Reset (Development Only)

```http
POST /admin/reset-db
Authorization: Bearer <jwt_token>
```

**Requirements:**

- Valid JWT authentication
- `system_role: "system_admin"` in user's `app_metadata`
- `APP_ENV != "production"`
- `ALLOW_DB_RESET=true` environment variable

**Response (200):**

```json
{
  "status": "success",
  "data": null,
  "message": "Database schema reset successfully",
  "meta": {
    "timestamp": "2023-05-18T12:34:56Z",
    "version": "1.0.0"
  }
}
```

**Security Notes:**

- Returns 404 in production environments
- All reset actions are logged and tracked in Sentry
- Only Hover operators should have system administrator access

## Error Handling

### Standard Error Codes

- `invalid_request` - Malformed request or missing required fields
- `authentication_required` - Missing or invalid authentication
- `permission_denied` - Insufficient permissions for operation
- `resource_not_found` - Requested resource doesn't exist
- `rate_limit_exceeded` - Too many requests
- `validation_failed` - Request data fails validation
- `server_error` - Internal server error
- `service_unavailable` - Service temporarily unavailable

### Field Validation Errors

For validation errors, the `details` object contains field-specific error
information:

```json
{
  "status": "error",
  "error": {
    "code": "validation_failed",
    "message": "Request validation failed",
    "details": {
      "domain": "Invalid domain format",
      "max_pages": "Must be between 1 and 1000"
    }
  },
  "meta": {
    "timestamp": "2023-05-18T12:34:56Z",
    "version": "1.0.0"
  }
}
```

## Rate Limiting

### Current Implementation

- **IP-based**: 5 requests per second per IP
- **Burst capacity**: 5 requests

### Planned Enhancement

- **User-based**: Different limits per authentication method
- **Endpoint-specific**: Different limits for different operations
- **Organisation-based**: Limits based on subscription tier

### Rate Limit Headers

```
X-RateLimit-Limit: 1000
X-RateLimit-Remaining: 999
X-RateLimit-Reset: 1684412345
X-RateLimit-Retry-After: 30
```

## Webhook System (Planned)

### Webhook Registration

```http
POST /v1/webhooks
Authorization: Bearer <token>

{
  "url": "https://example.com/webhooks/hover",
  "events": ["job.completed", "job.failed"],
  "secret": "webhook_secret_123"
}
```

### Webhook Events

#### Job Completed

```json
{
  "event": "job.completed",
  "timestamp": "2023-05-18T12:34:56Z",
  "data": {
    "job_id": "job_123abc",
    "domain": "example.com",
    "status": "completed",
    "total_tasks": 150,
    "completed_tasks": 150,
    "stats": {
      "avg_response_time": 234,
      "cache_hit_ratio": 0.85
    }
  },
  "signature": "sha256=..."
}
```

## Interface-Specific Considerations

### Slack Integration

- **Simplified responses**: Key information only
- **Interactive elements**: Buttons for common actions
- **Status updates**: Regular progress notifications

### Webflow Extension

- **Minimal payload**: Only essential data
- **Real-time updates**: WebSocket connection for progress
- **Site-specific defaults**: Remember settings per Webflow site

### CLI Tool

- **Bulk operations**: Support for multiple jobs
- **Detailed output**: Complete information for debugging
- **Local caching**: Store frequently accessed data

## Implementation Priority

### Phase 1: Standardise Existing ✅ (Completed)

1. ✅ Update response format for existing endpoints
2. ✅ Add proper error handling with consistent status codes
3. ✅ Implement standard authentication checks
4. ✅ Add request ID tracking and middleware stack
5. ✅ Create RESTful API structure (`/v1/*` endpoints)
6. ✅ Implement comprehensive middleware (CORS, logging, rate limiting)
7. ✅ Secure debug endpoints and move to admin namespace

### Phase 2: Complete CRUD Operations

1. Implement missing job management endpoints
2. Add task management endpoints
3. Create API key management
4. Add organisation endpoints

### Phase 3: Integration Features

1. Webhook system implementation
2. Advanced authentication (scoped API keys)
3. Rate limiting enhancements
4. Real-time updates via WebSockets

### Phase 4: Interface-Specific Optimisations

1. Slack-specific endpoints
2. Webflow extension optimisations
3. CLI tool bulk operations
4. Mobile app considerations

## Security Considerations

### Authentication Security

- JWT tokens with short expiry (15 minutes)
- API keys with scoped permissions
- Secure storage requirements documented
- Regular key rotation encouraged

### Request Security

- CORS properly configured
- Content Security Policy implemented
- Input validation on all endpoints
- SQL injection prevention
- XSS protection headers

### Data Privacy

- Organisation-level data isolation
- Row Level Security in PostgreSQL
- Audit logging for sensitive operations
- GDPR compliance features

## Monitoring & Observability

### Metrics to Track

- Request latency by endpoint
- Error rate by endpoint and error type
- Authentication failure rate
- Rate limit hit rate
- Job completion rate and time

### Logging

- Structured JSON logs
- Request ID correlation
- Error context preservation
- Performance metrics

### Telemetry Pipeline

- **Tracing**: OpenTelemetry spans are emitted for every HTTP request and worker
  task. Configure the OTLP HTTP exporter via:
  - `OBSERVABILITY_ENABLED=true` (default) to enable instrumentation
  - `OTEL_EXPORTER_OTLP_ENDPOINT=https://your-collector.example.com`
  - Optional headers with
    `OTEL_EXPORTER_OTLP_HEADERS=x-api-key=secret,tenant=bee`
  - `OTEL_EXPORTER_OTLP_INSECURE=true` when targeting a non-TLS endpoint
    (development only)
- **Metrics**: Prometheus-compatible metrics are exposed on `METRICS_ADDR`
  (default `:9464`) under `/metrics`. Example scrape configuration:

```yaml
- job_name: hover
  static_configs:
    - targets: ["hover.internal:9464"]
```

Worker task counters (`bee_worker_task_total`) and histograms
(`bee_worker_task_duration_ms`) augment the standard `otelhttp` request metrics.

- **Infrastructure note**: Production metrics are scraped by the Fly Alloy agent
  `bee-observability` (config in `~/fly-configs/bee-observability/config.alloy`)
  and remote-written to Grafana Cloud
  (`https://prometheus-prod-41-prod-au-southeast-1.grafana.net/api/prom/push`)
  with the stack-specific username + Primary API Token. The agent’s dedicated
  IPv4 must remain in the stack’s allow list. OTLP traces continue to flow
  directly to `https://otlp-gateway-prod-au-southeast-1.grafana.net`.

This API design provides a solid foundation for all current and future
interfaces while maintaining consistency, security, and scalability.
