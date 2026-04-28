import { Grid3x3 } from "lucide-react";
import { ViewPlaceholder } from "@/components/shell/ViewPlaceholder";

/*
 * Marketplace stub. The backend has no app marketplace endpoints; this
 * view mirrors that reality rather than mocking a fake app store.
 */
export default function AppsPage() {
  return (
    <ViewPlaceholder
      Icon={Grid3x3}
      title="Apps"
      body="The app marketplace isn't connected yet."
    />
  );
}
