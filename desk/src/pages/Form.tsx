import { useEffect, useState } from 'react';

import { isAddressable } from '../access';
import { ApiError, getDocTypeMeta, getDocumentWithTitles } from '../api';
import { DocumentForm } from '../components/DocumentForm';
import { PageHeader } from '../components/PageHeader';
import { Skeleton } from '../components/Skeleton';
import { NO_TITLES } from '../linkTitles';
import type { LinkTitles } from '../linkTitles';
import { listHref } from '../router';
import type { DocTypeMeta, DocumentRow } from '../types';

interface Props {
  doctype: string;
  /** The document's name, or null for "New". */
  docName: string | null;
  /** The logged-in user (for the rights that apply to the user's own documents only). */
  username: string;
}

type Load =
  | { status: 'loading' }
  | { status: 'error'; message: string }
  | { status: 'ready'; meta: DocTypeMeta; doc: DocumentRow | null; titles: LinkTitles };

const message = (e: unknown): string =>
  e instanceof ApiError ? e.message : 'Cannot reach the server.';

/** Loads the metadata (and the document) of the route, then shows the form. */
export function Form({ doctype, docName, username }: Props) {
  // A name that has no REST path is never requested.
  const addressable = docName === null || isAddressable(docName);
  const [load, setLoad] = useState<Load>({ status: 'loading' });

  useEffect(() => {
    if (!addressable) return;
    let cancelled = false;
    const work = async (): Promise<Load> => {
      const meta = await getDocTypeMeta(doctype);
      const titled = docName === null ? null : await getDocumentWithTitles(doctype, docName);
      return { status: 'ready', meta, doc: titled?.doc ?? null, titles: titled?.titles ?? NO_TITLES };
    };
    work().then(
      (next) => {
        if (!cancelled) setLoad(next);
      },
      (e: unknown) => {
        if (!cancelled) setLoad({ status: 'error', message: message(e) });
      },
    );
    return () => {
      cancelled = true;
    };
  }, [doctype, docName, addressable]);

  if (!addressable) {
    return (
      <div className="page">
        <PageHeader title={<h1>Cannot open this document</h1>} />
        <p role="alert" className="alert">
          The document name {JSON.stringify(docName)} cannot be opened in the desk: a REST path cannot
          address a document named "." or "..".
        </p>
        <p>
          <a href={listHref(doctype)}>Back to the list</a>
        </p>
      </div>
    );
  }
  if (load.status === 'loading') {
    return (
      <div className="page" aria-busy="true">
        <PageHeader title={<h1>Loading...</h1>} />
        <Skeleton lines={6} />
      </div>
    );
  }
  if (load.status === 'error') {
    return (
      <div className="page">
        <PageHeader title={<h1>Cannot open this page</h1>} />
        <p role="alert" className="alert">
          {load.message}
        </p>
        <p>
          <a href={listHref(doctype)}>Back to the list</a>
        </p>
      </div>
    );
  }
  return <DocumentForm meta={load.meta} initial={load.doc} initialTitles={load.titles} username={username} />;
}
