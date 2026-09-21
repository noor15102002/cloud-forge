"use strict";

const { Client } = require("pg");

async function main() {
  if (process.argv.includes("--fail")) {
    // Deliberately print generated test material: CloudForge must omit it from
    // public command evidence. The fixture harness checks actual run values.
    console.error(process.env.APP_TEST_KEY || "missing-test-key");
    throw new Error("deliberate preparation failure");
  }
  const client = new Client({ connectionString: process.env.DATABASE_URL, connectionTimeoutMillis: 2000, query_timeout: 5000 });
  try {
    await client.connect();
    await client.query("CREATE EXTENSION IF NOT EXISTS vector");
    await client.query("CREATE TABLE IF NOT EXISTS cloudforge_probe (id integer PRIMARY KEY, embedding vector(3) NOT NULL)");
    await client.query("INSERT INTO cloudforge_probe VALUES (1, '[3,4,0]') ON CONFLICT (id) DO NOTHING");
    const result = await client.query("SELECT embedding <-> '[0,0,0]'::vector AS distance FROM cloudforge_probe WHERE id = 1");
    if (Number(result.rows[0]?.distance) !== 5) throw new Error("vector preparation did not produce expected value");
    console.log("schema-prepared");
  } finally {
    await client.end();
  }
}

main().catch(() => {
  console.error("preparation-failed");
  process.exitCode = 1;
});
