/** A grey placeholder block while data loads (it shimmers unless the user prefers less motion). */
export function Skeleton({ lines = 3 }: { lines?: number }) {
  return (
    <div className="skeleton-group" aria-hidden="true">
      {Array.from({ length: lines }, (_, i) => (
        <div key={i} className={`skeleton skeleton-${i % 3}`} />
      ))}
    </div>
  );
}
