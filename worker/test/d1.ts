// A minimal D1 stand-in over node:sqlite, enough for Store.
import { readFileSync } from "node:fs";
import { DatabaseSync } from "node:sqlite";
import type { Database, Statement } from "../src/store";

class FakeStatement implements Statement {
  constructor(
    private readonly db: DatabaseSync,
    private readonly sql: string,
    private readonly params: unknown[] = [],
  ) {}

  bind(...values: unknown[]): Statement {
    return new FakeStatement(this.db, this.sql, values);
  }

  private args() {
    return this.params as (string | number | null)[];
  }

  async first<T>(): Promise<T | null> {
    const rows = this.db.prepare(this.sql).all(...this.args());
    return (rows[0] as T) ?? null;
  }

  async all<T>(): Promise<{ results: T[] }> {
    return { results: this.db.prepare(this.sql).all(...this.args()) as T[] };
  }

  async run(): Promise<{ meta: { changes: number } }> {
    const res = this.db.prepare(this.sql).run(...this.args());
    return { meta: { changes: Number(res.changes) } };
  }
}

export class FakeD1 implements Database {
  readonly db = new DatabaseSync(":memory:");

  constructor() {
    this.db.exec("PRAGMA foreign_keys = ON");
    this.db.exec(readFileSync(new URL("../migrations/0001_init.sql", import.meta.url), "utf8"));
  }

  prepare(sql: string): Statement {
    return new FakeStatement(this.db, sql);
  }

  async batch(statements: Statement[]): Promise<{ meta: { changes: number } }[]> {
    this.db.exec("BEGIN");
    try {
      const out = [];
      for (const s of statements) out.push(await s.run());
      this.db.exec("COMMIT");
      return out;
    } catch (err) {
      this.db.exec("ROLLBACK");
      throw err;
    }
  }
}
