from django.views.generic import ListView
from .models import Transaction

class Overview(ListView):
    model = Transaction
    template_name = "uploader/success.html"
    context_object_name = "imported_data"
