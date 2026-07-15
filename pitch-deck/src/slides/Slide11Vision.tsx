import { slides } from "../slides";
import { SlideLayout } from "../components/SlideLayout";

/** Slide 11: Vision. Thin wrapper — selects its data, renders the layout. */
const data = slides.find((s) => s.id === "vision")!;
export default function Slide11Vision() {
  return <SlideLayout data={data} />;
}
