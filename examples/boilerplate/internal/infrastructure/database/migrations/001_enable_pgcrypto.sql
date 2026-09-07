-- +goose Up
-- Enables gen_random_uuid(), used by this application's own tables to
-- generate primary keys server-side. Every new service built from this
-- boilerplate is expected to add its own migrations here, numbered 002,
-- 003, ... — see the "Agregar un módulo de negocio nuevo" section of the
-- README.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- +goose Down
DROP EXTENSION IF EXISTS pgcrypto;
