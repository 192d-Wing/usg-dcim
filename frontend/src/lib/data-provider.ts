/**
 * Custom Refine DataProvider that maps Refine's resource calls to our FastAPI shape.
 *
 * - Our list endpoints return { items, page, page_size, total, has_more }
 * - Our detail endpoints return the entity directly
 * - Filters/sorts go in querystring as `field=value` and `sort=name&order=asc`
 * - Resource names map directly to URL paths under /api/v1, with namespace prefixes
 *   so callers can address e.g. "inventory/sites" or "alerts" cleanly.
 */
import type {
  CrudFilters, CrudOperators, CrudSorting, DataProvider,
} from '@refinedev/core';
import { http } from './http';

const baseURL = '/api/v1';

function buildQuery(
  pagination: { current?: number; pageSize?: number } | undefined,
  filters?: CrudFilters,
  sorters?: CrudSorting,
): URLSearchParams {
  const q = new URLSearchParams();
  if (pagination) {
    q.set('page', String(pagination.current ?? 1));
    q.set('page_size', String(pagination.pageSize ?? 50));
  }
  if (sorters?.length) {
    q.set('sort', sorters[0].field);
    q.set('order', sorters[0].order);
  }
  if (filters?.length) {
    for (const f of filters) {
      if (!('field' in f)) continue;
      const op = f.operator as CrudOperators;
      // Backend uses simple equality filters via querystring. Anything else we drop or coerce.
      if (op === 'eq' || op === 'contains') {
        if (f.value !== undefined && f.value !== null && f.value !== '') {
          q.set(f.field, String(f.value));
        }
      }
    }
  }
  return q;
}

export const dataProvider: DataProvider = {
  getApiUrl: () => baseURL,

  async getList({ resource, pagination, filters, sorters }) {
    const q = buildQuery(pagination, filters, sorters);
    const r = await http.get(`/${resource}?${q.toString()}`);
    return { data: r.data.items ?? [], total: r.data.total ?? (r.data.items?.length ?? 0) };
  },

  async getOne({ resource, id }) {
    const r = await http.get(`/${resource}/${id}`);
    return { data: r.data };
  },

  async create({ resource, variables }) {
    const r = await http.post(`/${resource}`, variables);
    return { data: r.data };
  },

  async update({ resource, id, variables }) {
    const r = await http.patch(`/${resource}/${id}`, variables);
    return { data: r.data };
  },

  async deleteOne({ resource, id }) {
    const r = await http.delete(`/${resource}/${id}`);
    return { data: r.data };
  },

  async getMany({ resource, ids }) {
    // We don't have a batch endpoint; loop in parallel and let the caller cache.
    const rs = await Promise.all(ids.map((id) => http.get(`/${resource}/${id}`)));
    return { data: rs.map((r) => r.data) };
  },

  async custom({ url, method, payload, query, headers }) {
    const r = await http.request({
      url,
      baseURL,
      method,
      data: payload,
      params: query,
      headers,
    });
    return { data: r.data };
  },
};
