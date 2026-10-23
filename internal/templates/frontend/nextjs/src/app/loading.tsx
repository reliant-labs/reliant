export default function Loading() {
  return (
    <main className="flex flex-1 flex-col items-center justify-center p-8">
      <div className="w-full max-w-2xl animate-pulse motion-reduce:animate-none space-y-4">
        <div className="mx-auto h-8 w-2/3 rounded-md bg-border" />
        <div className="mx-auto h-4 w-1/2 rounded-md bg-border" />
        <div className="mx-auto h-4 w-3/4 rounded-md bg-border" />
        <div className="flex justify-center gap-4 pt-4">
          <div className="h-9 w-28 rounded-md bg-border" />
          <div className="h-9 w-28 rounded-md bg-border" />
        </div>
      </div>
    </main>
  );
}
