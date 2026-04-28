import { Sparkles } from "lucide-react";
import { ViewPlaceholder } from "@/components/shell/ViewPlaceholder";

/*
 * AI view — the backend has no AI endpoints yet (the audit turned up no
 * transcription / summarization / assistant surface), so this page shows
 * an informative empty state. The Aloqa reference has three AI tabs
 * (Search / Summaries / Assistant); we'll stand those up when the backend
 * grows them.
 */
export default function AIPage() {
  return (
    <ViewPlaceholder
      Icon={Sparkles}
      title="AI features"
      body="Transcription, summaries, and assistant aren't wired up on the backend yet. They'll show up here when they are."
    />
  );
}
