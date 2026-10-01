-- +goose Up
-- Server-side paging of the requests table (go-tangra specs/032): the default
-- sort (start date, newest first) with the id tie-breaker across a whole
-- tenant. The other sort fields of requests, and every allowance sort, page
-- within one tenant's rows that the existing indexes already narrow.
CREATE INDEX IF NOT EXISTS hr_requests_list ON hr_requests (tenant_id, start_date DESC, id DESC);

-- +goose Down
DROP INDEX IF EXISTS hr_requests_list;
